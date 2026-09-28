package dialog

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func mockClients(t *testing.T, now time.Time) (*dataapi.Client, *dataapi.WorkerClient, func()) {
	return mockClientsClock(t, func() time.Time { return now })
}

func mockClientsClock(t *testing.T, now func() time.Time) (*dataapi.Client, *dataapi.WorkerClient, func()) {
	t.Helper()
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", filepath.Join(t.TempDir(), "snapshot.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	actor, err := dataapi.New(dataapi.Config{BaseURL: server.URL + "/internal/v1", Token: "synthetic-service-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "synthetic-worker-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return actor, worker, server.Close
}

func TestMathAnswerRejectsOtherActorAndExpiredHold(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicle := page.Items[0]
	if _, err := actor.CheckoutCreate(context.Background(), driver, vehicle.ID, vehicle.Version, "expiry-setup-hold", nil); err != nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "expiry-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[0]
	event := callbackItem(driver, "expiry-math", menu.Buttons[0][0].Payload, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "expiry-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
		t.Fatalf("math creation = %+v %v", result, err)
	}
	question := sender.Messages()[1]
	if len(question.Buttons) != 4 {
		t.Fatalf("math question = %+v", question)
	}
	answerPayload := question.Buttons[0][0].Payload
	other := callbackItem("8000000000000000002", "other-answer", answerPayload, now)
	if err := processor.Handle(context.Background(), other); err != nil || !strings.Contains(sender.Messages()[2].Text, "Шаг подтверждения изменился") {
		t.Fatalf("other actor answer = %v, %+v", err, sender.Messages())
	}
	current = now.Add(16 * time.Minute)
	expired := callbackItem(driver, "expired-answer", answerPayload, current)
	if err := processor.Handle(context.Background(), expired); err != nil || !strings.Contains(sender.Messages()[3].Text, "hold истёк") {
		t.Fatalf("expired answer = %v, %+v", err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Trip != nil {
		t.Fatalf("expired hold state = %+v %v", state, err)
	}
}

func menuItem(actor, key string, now time.Time) dataapi.InboxClaimItem {
	command := "/menu"
	return dataapi.InboxClaimItem{ID: key, Event: dataapi.NormalizedEvent{
		IntegrationKey: "demo-bot", EventKey: "message:" + key + ":message_created", EventType: "message_created",
		ActorMaxUserID: actor, ChatID: actor, MessageID: &key, OccurredAt: now,
		Payload: dataapi.NormalizedPayload{Kind: "text", Text: &command},
	}}
}

func TestBootstrapUsesDataAPIAccessForMenu(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, MAX: sender}
	for _, id := range []string{"8000000000000000001", "8000000000000000003", "8000000000000000004", "8000000000000000009"} {
		if err := processor.Handle(context.Background(), menuItem(id, "menu-"+id, now)); err != nil {
			t.Fatalf("menu for %s: %v", id, err)
		}
	}
	messages := sender.Messages()
	if len(messages) != 4 {
		t.Fatalf("menu count = %d", len(messages))
	}
	if !strings.Contains(messages[0].Text, "Доступные автомобили") || strings.Contains(messages[0].Text, "Управление автопарком") {
		t.Fatalf("driver menu = %q", messages[0].Text)
	}
	if !strings.Contains(messages[1].Text, "Управление автопарком") {
		t.Fatalf("admin menu = %q", messages[1].Text)
	}
	if strings.Contains(messages[2].Text, "Доступные автомобили") || !strings.Contains(messages[2].Text, "Мои поездки") {
		t.Fatalf("blocked driver menu = %q", messages[2].Text)
	}
	if strings.Contains(messages[3].Text, "MAX Fleet") || !strings.Contains(messages[3].Text, "8000000000000000009") {
		t.Fatalf("unknown actor response = %q", messages[3].Text)
	}
}

func TestBootstrapDefersOtherEventsWithoutAccessOrSend(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	item := menuItem("8000000000000000001", "photo-1", now)
	item.Event.Payload = dataapi.NormalizedPayload{Kind: "photo"}
	if err := (Bootstrap{}).Handle(context.Background(), item); !errors.Is(err, inboxworker.ErrDeferred) {
		t.Fatalf("photo was not deferred: %v", err)
	}
	item.Event.Payload = dataapi.NormalizedPayload{Kind: "callback"}
	item.Event.EventType = "message_callback"
	if err := (Bootstrap{}).Handle(context.Background(), item); !errors.Is(err, inboxworker.ErrDeferred) {
		t.Fatalf("callback was not deferred: %v", err)
	}
}

func TestMenuKeepsExistingTripOrCheckoutAheadOfNewVehicle(t *testing.T) {
	employee := dataapi.Employee{Role: "employee", CanStartTrip: true}
	for _, test := range []struct {
		state dataapi.CurrentState
		want  string
	}{
		{dataapi.CurrentState{Checkout: &dataapi.Checkout{}}, "Продолжить оформление"},
		{dataapi.CurrentState{Trip: &dataapi.Trip{}}, "Текущая поездка"},
	} {
		menu := menuText(employee, test.state)
		if !strings.Contains(menu, test.want) || strings.Contains(menu, "Доступные автомобили") {
			t.Fatalf("unfinished flow menu = %q", menu)
		}
	}
	started := dataapi.NormalizedEvent{EventType: "bot_started", Payload: dataapi.NormalizedPayload{Kind: "start"}}
	if !isMenuEvent(started) {
		t.Fatal("bot_started did not open the menu")
	}
}

func TestBootstrapWorkerAcksMenuAndRejectsPhotoWithoutInspection(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	id := "8000000000000000001"
	menu := menuItem(id, "menu-first", now).Event
	photoKey := "photo-second"
	photoSource := "synthetic-photo-source"
	photo := dataapi.NormalizedEvent{IntegrationKey: "demo-bot", EventKey: "message:" + photoKey + ":message_created", EventType: "message_created", ActorMaxUserID: id, ChatID: id, MessageID: &photoKey, OccurredAt: now,
		Payload: dataapi.NormalizedPayload{Kind: "photo", PhotoSourceKey: &photoSource, AttachmentCount: 1}}
	for _, event := range []dataapi.NormalizedEvent{menu, photo} {
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
	}
	sender := &maxsdk.RecordingTransport{}
	worker := inboxworker.Worker{ID: "dialog-bootstrap-worker", Store: store, Processor: Bootstrap{Data: actor, MAX: sender}, Now: func() time.Time { return now }}
	first, err := worker.RunOnce(context.Background(), 10)
	if err != nil || first.Acked != 1 || len(sender.Messages()) != 1 {
		t.Fatalf("menu cycle = %+v, %v", first, err)
	}
	second, err := worker.RunOnce(context.Background(), 10)
	if err != nil || second.Acked != 1 || second.Deferred != 0 || len(sender.Messages()) != 2 || !strings.Contains(sender.Messages()[1].Text, "нет активного шага загрузки фото") {
		t.Fatalf("photo cycle = %+v, %v", second, err)
	}
}

func TestCatalogPagesFiveWithoutLocalCursorAndRechecksAccess(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, MAX: sender}
	const driver = "8000000000000000001"
	item := menuItem(driver, "cars-first", now)
	command := "/cars"
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	first := sender.Messages()[0].Text
	if strings.Count(first, "DEMO-") != 5 || !strings.Contains(first, "Далее: /cars 2") || strings.Contains(first, "ключ") {
		t.Fatalf("first catalog page = %q", first)
	}
	// A fresh processor reconstructs page two through the DataAPI cursor chain.
	processor = Bootstrap{Data: actor, MAX: sender}
	command = "/cars 2"
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	second := sender.Messages()[1].Text
	if strings.Count(second, "DEMO-") != 5 || !strings.Contains(second, "Назад: /cars 1") || strings.Contains(second, "Далее:") {
		t.Fatalf("second catalog page = %q", second)
	}
	command = "/cars 3"
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil || !strings.Contains(sender.Messages()[2].Text, "Список изменился") {
		t.Fatalf("stale page = %v, %+v", err, sender.Messages())
	}
	blocked := menuItem("8000000000000000004", "cars-blocked", now)
	command = "/cars"
	blocked.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), blocked); err != nil || !strings.Contains(sender.Messages()[3].Text, "нельзя начать") {
		t.Fatalf("blocked catalog = %v, %+v", err, sender.Messages())
	}
	unknown := menuItem("8000000000000000009", "cars-unknown", now)
	unknown.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), unknown); err != nil || strings.Contains(sender.Messages()[4].Text, "DEMO-") {
		t.Fatalf("unknown catalog = %v, %+v", err, sender.Messages())
	}
	available := true
	vehicles, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(vehicles.Items) == 0 {
		t.Fatalf("seed vehicles = %+v, %v", vehicles, err)
	}
	vehicle := vehicles.Items[0]
	if _, err := actor.CheckoutCreate(context.Background(), driver, vehicle.ID, vehicle.Version, "catalog-hold", nil); err != nil {
		t.Fatal(err)
	}
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil || !strings.Contains(sender.Messages()[5].Text, "нельзя начать") {
		t.Fatalf("active checkout catalog = %v, %+v", err, sender.Messages())
	}
}

type emptyCatalogReader struct{}

func (emptyCatalogReader) Me(_ context.Context, actor string) (dataapi.Me, error) {
	return dataapi.Me{Allowed: true, MaxUserID: actor, Employee: &dataapi.Employee{CanStartTrip: true}}, nil
}
func (emptyCatalogReader) State(context.Context, string) (dataapi.CurrentState, error) {
	return dataapi.CurrentState{}, nil
}
func (emptyCatalogReader) CurrentRules(context.Context, string) (dataapi.Rules, error) {
	return dataapi.Rules{}, errors.New("unexpected rules read")
}
func (emptyCatalogReader) Vehicles(context.Context, string, dataapi.VehicleFilter) (dataapi.Page[dataapi.Vehicle], error) {
	return dataapi.Page[dataapi.Vehicle]{}, nil
}
func (emptyCatalogReader) Vehicle(context.Context, string, string) (dataapi.Vehicle, error) {
	return dataapi.Vehicle{}, errors.New("unexpected vehicle read")
}
func (emptyCatalogReader) PreviousInspection(context.Context, string, string) (dataapi.Inspection, error) {
	return dataapi.Inspection{}, errors.New("unexpected previous inspection read")
}
func (emptyCatalogReader) Issue(context.Context, string, string) (dataapi.Issue, error) {
	return dataapi.Issue{}, errors.New("unexpected issue read")
}

func TestCatalogEmptyListMessage(t *testing.T) {
	sender := &maxsdk.RecordingTransport{}
	command := "/cars"
	item := menuItem("8000000000000000001", "cars-empty", time.Now())
	item.Event.Payload.Text = &command
	if err := (Bootstrap{Data: emptyCatalogReader{}, MAX: sender}).Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if got := sender.Messages()[0].Text; got != "Сейчас нет доступных автомобилей. Попробуйте обновить список позже." {
		t.Fatalf("empty catalog = %q", got)
	}
}

func TestCardReadsFreshVehicleAndHandlesInvalidOrStaleLink(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("available vehicles = %+v, %v", page, err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, MAX: sender, Location: time.FixedZone("MSK", 3*3600)}
	item := menuItem(driver, "card-open", now)
	command := "/car " + page.Items[0].ID
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	card := sender.Messages()[0].Text
	for _, required := range []string{page.Items[0].Plate, "Место парковки:", "Место подтверждено:", "MSK", "Топливо:", "Пробег:", "Ключи:", "К списку: /cars"} {
		if !strings.Contains(card, required) {
			t.Fatalf("card misses %q: %q", required, card)
		}
	}
	if strings.Contains(card, "Тестовый сотрудник") || strings.Contains(card, "Взять машину") {
		t.Fatalf("card leaked another actor or unfinished action: %q", card)
	}
	if buttons := sender.Messages()[0].Buttons; len(buttons) != 4 || buttons[1][0].Text != "Показать на карте" || !strings.HasPrefix(buttons[1][0].URL, "https://www.openstreetmap.org/?mlat=") {
		t.Fatalf("confirmed parking map button = %+v", buttons)
	}
	command = "/car invalid"
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil || !strings.Contains(sender.Messages()[1].Text, "Некорректная ссылка") {
		t.Fatalf("invalid card = %v, %+v", err, sender.Messages())
	}
	command = "/car 10000000-0000-4000-8000-000000000999"
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil || !strings.Contains(sender.Messages()[2].Text, "больше не доступен") {
		t.Fatalf("missing card = %v, %+v", err, sender.Messages())
	}
	unknown := menuItem("8000000000000000009", "card-unknown", now)
	unknown.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), unknown); err != nil || strings.Contains(sender.Messages()[3].Text, "Ключи:") {
		t.Fatalf("unknown actor card = %v, %+v", err, sender.Messages())
	}
}

type noTripAdminReader struct{ emptyCatalogReader }

func (noTripAdminReader) Me(_ context.Context, actor string) (dataapi.Me, error) {
	return dataapi.Me{Allowed: true, MaxUserID: actor, Employee: &dataapi.Employee{Role: "admin", CanStartTrip: false}}, nil
}
func (noTripAdminReader) Vehicle(_ context.Context, _, id string) (dataapi.Vehicle, error) {
	return dataapi.Vehicle{ID: id, Status: "available", KeyInstructions: "У диспетчера", CurrentParking: &dataapi.ParkingLocation{ConfirmedAt: time.Now()}}, nil
}

func TestAdminWithoutTripRightSeesNoCheckoutOffer(t *testing.T) {
	const vehicleID = "10000000-0000-4000-8000-000000000001"
	sender := &maxsdk.RecordingTransport{}
	item := menuItem("8000000000000000003", "admin-no-trip", time.Now())
	command := "/car " + vehicleID
	item.Event.Payload.Text = &command
	if err := (Bootstrap{Data: noTripAdminReader{}, MAX: sender}).Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if got := sender.Messages()[0].Text; !strings.Contains(got, "Выдача: недоступна") {
		t.Fatalf("admin without trip permission card = %q", got)
	}
}

func TestCardShowsMissingFieldsAsUnknown(t *testing.T) {
	vehicle := dataapi.Vehicle{Plate: "DEMO-NEW", Status: "unavailable"}
	card := cardText(vehicle, time.UTC)
	for _, required := range []string{"Место парковки: Не указано", "Топливо: Не указано", "Пробег: Не указано", "Описание: Не указано", "Ключи: Не указано"} {
		if !strings.Contains(card, required) {
			t.Fatalf("card misses %q: %q", required, card)
		}
	}
}

func TestCardAvailabilityRequiresAccessParkingAndKeys(t *testing.T) {
	vehicle := dataapi.Vehicle{Status: "available", KeyInstructions: "У диспетчера", CurrentParking: &dataapi.ParkingLocation{ConfirmedAt: time.Now()}}
	employee := dataapi.Employee{CanStartTrip: true}
	if got := checkoutAvailabilityText(vehicle, employee, dataapi.CurrentState{}); !strings.Contains(got, "доступно подтверждение") {
		t.Fatalf("ready vehicle = %q", got)
	}
	withoutParking := vehicle
	withoutParking.CurrentParking = nil
	if got := checkoutAvailabilityText(withoutParking, employee, dataapi.CurrentState{}); !strings.Contains(got, "требуется подтверждённая парковка") {
		t.Fatalf("missing parking = %q", got)
	}
	withoutKeys := vehicle
	withoutKeys.KeyInstructions = " \n "
	if got := checkoutAvailabilityText(withoutKeys, employee, dataapi.CurrentState{}); !strings.Contains(got, "инструкция по ключам") {
		t.Fatalf("missing keys = %q", got)
	}
	blocked := employee
	blocked.CanStartTrip = false
	if got := checkoutAvailabilityText(vehicle, blocked, dataapi.CurrentState{}); !strings.Contains(got, "недоступна для текущего пользователя") {
		t.Fatalf("blocked employee = %q", got)
	}
	if got := checkoutAvailabilityText(vehicle, employee, dataapi.CurrentState{Checkout: &dataapi.Checkout{}}); !strings.Contains(got, "текущий сценарий") {
		t.Fatalf("active checkout = %q", got)
	}
	underReview := vehicle
	underReview.NeedsReview = true
	if got := checkoutAvailabilityText(underReview, employee, dataapi.CurrentState{}); !strings.Contains(got, "автомобиль сейчас недоступен") {
		t.Fatalf("needs review = %q", got)
	}
}

func callbackItem(actor, key, payload string, now time.Time) dataapi.InboxClaimItem {
	return dataapi.InboxClaimItem{ID: key, Event: dataapi.NormalizedEvent{
		IntegrationKey: "demo-bot", EventKey: "callback:" + key + ":message_callback", EventType: "message_callback",
		ActorMaxUserID: actor, ChatID: actor, CallbackID: &key, OccurredAt: now,
		Payload: dataapi.NormalizedPayload{Kind: "callback", CallbackData: &payload},
	}}
}

func TestCatalogCallbacksUseClickerAndRefreshStaleCard(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, MAX: sender}
	const driver = "8000000000000000001"
	if err := processor.Handle(context.Background(), callbackItem(driver, "menu-callback", "menu", now)); err != nil {
		t.Fatal(err)
	}
	if got := sender.AnsweredCallbacks(); len(got) != 1 || got[0] != "menu-callback" {
		t.Fatalf("menu callback answer = %v", got)
	}
	menu := sender.Messages()[0]
	if len(menu.Buttons) != 2 || menu.Buttons[0][0].Payload != "cars:1" || menu.Buttons[1][0].Payload != "trip-list:mine:1" {
		t.Fatalf("menu buttons = %+v", menu.Buttons)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "cars-callback", "cars:1", now)); err != nil {
		t.Fatal(err)
	}
	catalog := sender.Messages()[1]
	if len(catalog.Buttons) != 6 || len(catalog.Buttons[5]) != 2 || catalog.Buttons[5][1].Payload != "cars:2" {
		t.Fatalf("catalog buttons = %+v", catalog.Buttons)
	}
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("available vehicles = %+v, %v", page, err)
	}
	vehicle := page.Items[0]
	stalePayload := catalog.Buttons[0][0].Payload
	if stalePayload != "car:"+vehicle.ID+":"+strconv.FormatInt(vehicle.Version, 10) {
		t.Fatalf("card button does not carry version: %q", stalePayload)
	}
	const otherDriver = "8000000000000000002"
	if _, err := actor.CheckoutCreate(context.Background(), otherDriver, vehicle.ID, vehicle.Version, "stale-card-hold", nil); err != nil {
		t.Fatal(err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "stale-card", stalePayload, now)); err != nil {
		t.Fatal(err)
	}
	card := sender.Messages()[2]
	if !strings.Contains(card.Text, "Данные автомобиля изменились") || !strings.Contains(card.Text, "Статус:") || len(card.Buttons) != 3 || card.Buttons[0][0].URL == "" || card.Buttons[1][0].Payload != "prev:"+vehicle.ID || card.Buttons[2][0].Payload != "cars:1" {
		t.Fatalf("stale card = %+v", card)
	}
	unknown := callbackItem("8000000000000000009", "other-clicker", stalePayload, now)
	if err := processor.Handle(context.Background(), unknown); err != nil {
		t.Fatal(err)
	}
	denied := sender.Messages()[3]
	if !strings.Contains(denied.Text, "8000000000000000009") || strings.Contains(denied.Text, "Ключи:") || len(denied.Buttons) != 0 {
		t.Fatalf("callback actor leaked card = %+v", denied)
	}
	if got := sender.AnsweredCallbacks(); len(got) != 4 || got[1] != "cars-callback" || got[2] != "stale-card" || got[3] != "other-clicker" {
		t.Fatalf("callback answers = %v", got)
	}
}

type previousReader struct {
	emptyCatalogReader
	inspection dataapi.Inspection
	actor      string
}

func (r *previousReader) PreviousInspection(_ context.Context, actor, _ string) (dataapi.Inspection, error) {
	r.actor = actor
	return r.inspection, nil
}

func TestPreviousInspectionShowsOnlyProjectionAndRechecksActor(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicleID := page.Items[0].ID
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, MAX: sender}
	if err := processor.Handle(context.Background(), callbackItem(driver, "prev-none", "prev:"+vehicleID, now)); err != nil {
		t.Fatal(err)
	}
	if got := sender.Messages()[0].Text; !strings.Contains(got, "пока нет") {
		t.Fatalf("no previous inspection = %q", got)
	}
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000009", "prev-unknown", "prev:"+vehicleID, now)); err != nil {
		t.Fatal(err)
	}
	if got := sender.Messages()[1].Text; !strings.Contains(got, "8000000000000000009") || strings.Contains(got, "осмотра") {
		t.Fatalf("unknown actor response = %q", got)
	}
	if got := sender.AnsweredCallbacks(); len(got) != 2 {
		t.Fatalf("previous callback answers = %v", got)
	}
	reader := &previousReader{inspection: dataapi.Inspection{ID: "30000000-0000-4000-8000-000000000002", Phase: "after", Status: "finalized", UpdatedAt: now, FuelLevel: intPointer(65), OdometerKM: int64Pointer(12000), OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}}}
	privateSender := &maxsdk.RecordingTransport{}
	if err := (Bootstrap{Data: reader, MAX: privateSender, Location: time.FixedZone("MSK", 3*3600)}).Handle(context.Background(), callbackItem(driver, "prev-finalized", "prev:"+vehicleID, now)); err != nil {
		t.Fatal(err)
	}
	view := privateSender.Messages()[0].Text
	for _, want := range []string{"Предыдущий завершённый осмотр", "65%", "12000 км", "8 из 8", "MSK"} {
		if !strings.Contains(view, want) {
			t.Fatalf("projection misses %q: %q", want, view)
		}
	}
	if strings.Contains(view, driver) || strings.Contains(view, reader.inspection.ID) {
		t.Fatalf("projection exposed actor or ID: %q", view)
	}
	if reader.actor != driver {
		t.Fatalf("projection read for %q", reader.actor)
	}
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }

type failedAnswerTransport struct{ maxsdk.RecordingTransport }

func (*failedAnswerTransport) AnswerCallback(context.Context, string) error {
	return errors.New("MAX answer unavailable")
}

func TestCallbackAnswerFailureDoesNotReplayDeliveredView(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	sender := &failedAnswerTransport{}
	item := callbackItem("8000000000000000001", "click-answer-fails", "menu", now)
	if err := (Bootstrap{Data: emptyCatalogReader{}, MAX: sender}).Handle(context.Background(), item); err != nil {
		t.Fatalf("callback answer failure retried a delivered view: %v", err)
	}
	if len(sender.Messages()) != 1 {
		t.Fatalf("delivered views = %d", len(sender.Messages()))
	}
}

func TestMalformedCatalogCallbackIsAnsweredAndAcked(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	item := callbackItem("8000000000000000001", "bad-catalog-page", "cars:999999999999999999999", now)
	if _, err := store.StoreInbox(context.Background(), item.Event, maxsdk.InboxIdempotencyKey(item.Event)); err != nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	worker := inboxworker.Worker{ID: "bad-catalog-worker", Store: store, Processor: Bootstrap{Data: actor, MAX: sender}, Now: func() time.Time { return now }}
	result, err := worker.RunOnce(context.Background(), 1)
	if err != nil || result.Acked != 1 || result.Deferred != 0 {
		t.Fatalf("malformed callback worker = %+v, %v", result, err)
	}
	messages := sender.Messages()
	if len(messages) != 1 || !strings.Contains(messages[0].Text, "повреждена") || len(messages[0].Buttons) != 1 || messages[0].Buttons[0][0].Payload != "cars:1" {
		t.Fatalf("malformed callback response = %+v", messages)
	}
	if got := sender.AnsweredCallbacks(); len(got) != 1 || got[0] != "bad-catalog-page" {
		t.Fatalf("malformed callback answer = %v", got)
	}
}

func TestCheckoutIntentCreatesHoldOnlyAfterConfirmedInboxEvent(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicle := page.Items[0]
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Location: time.UTC}
	card := callbackItem(driver, "take-card", "car:"+vehicle.ID+":"+strconv.FormatInt(vehicle.Version, 10), now)
	if err := processor.Handle(context.Background(), card); err != nil {
		t.Fatal(err)
	}
	buttons := sender.Messages()[0].Buttons
	if len(buttons) != 4 || !strings.HasPrefix(buttons[0][0].Payload, "intent:") {
		t.Fatalf("ready card buttons = %+v", buttons)
	}
	intent := callbackItem(driver, "take-intent", buttons[0][0].Payload, now)
	if err := processor.Handle(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	confirmation := sender.Messages()[1]
	if !strings.Contains(confirmation.Text, "15 минут") || len(confirmation.Buttons) != 2 || !strings.HasPrefix(confirmation.Buttons[0][0].Payload, "take:") {
		t.Fatalf("confirmation view = %+v", confirmation)
	}
	before, err := actor.State(context.Background(), driver)
	if err != nil || before.Checkout != nil || before.Trip != nil {
		t.Fatalf("intent changed domain state: %+v %v", before, err)
	}
	event := callbackItem(driver, "take-confirm", confirmation.Buttons[0][0].Payload, now).Event
	if err := processor.Handle(context.Background(), callbackItem(driver, "take-without-lease", confirmation.Buttons[0][0].Payload, now)); err == nil {
		t.Fatal("confirmed hold without durable inbox lease")
	}
	before, err = actor.State(context.Background(), driver)
	if err != nil || before.Checkout != nil {
		t.Fatalf("unleased confirmation changed domain state: %+v %v", before, err)
	}
	stored, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event))
	if err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "checkout-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	result, err := worker.RunOnce(context.Background(), 1)
	if err != nil || result.Acked != 1 || result.Retried != 0 {
		t.Fatalf("confirmed hold = %+v %v", result, err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Trip != nil {
		t.Fatalf("hold state = %+v %v", state, err)
	}
	if got := state.Checkout.ExpiresAt; !got.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("hold expiry = %s", got)
	}
	if !strings.Contains(sender.Messages()[2].Text, "зарезервирована до") {
		t.Fatalf("hold message = %+v", sender.Messages()[2])
	}
	duplicate, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event))
	if err != nil || !duplicate.Duplicate || duplicate.ID != stored.ID {
		t.Fatalf("duplicate inbox event = %+v %v", duplicate, err)
	}
	second, err := worker.RunOnce(context.Background(), 1)
	if err != nil || second.Claimed != 0 || len(sender.Messages()) != 3 {
		t.Fatalf("duplicate replay = %+v %v", second, err)
	}
}

func TestCheckoutRejectsStaleAndUnknownActorBeforeCommand(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicle := page.Items[0]
	payload := "take:" + vehicle.ID + ":" + strconv.FormatInt(vehicle.Version, 10)
	if _, err := actor.CheckoutCreate(context.Background(), "8000000000000000002", vehicle.ID, vehicle.Version, "other-driver-hold", nil); err != nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	stale := callbackItem(driver, "stale-take", payload, now)
	if err := processor.Handle(context.Background(), stale); err != nil || !strings.Contains(sender.Messages()[0].Text, "изменились") {
		t.Fatalf("stale take = %v, %+v", err, sender.Messages())
	}
	unknown := callbackItem("8000000000000000009", "unknown-take", payload, now)
	if err := processor.Handle(context.Background(), unknown); err != nil || strings.Contains(sender.Messages()[1].Text, vehicle.Plate) {
		t.Fatalf("unknown take = %v, %+v", err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil {
		t.Fatalf("stale/unknown actor created hold: %+v %v", state, err)
	}
}

func TestBE03GoldenMenuCatalogAndCard(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	const vehicleID = "10000000-0000-4000-8000-000000000001"
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Location: time.FixedZone("MSK", 3*3600)}
	if err := processor.Handle(context.Background(), menuItem(driver, "golden-menu", now)); err != nil {
		t.Fatal(err)
	}
	command := "/cars"
	item := menuItem(driver, "golden-cars", now)
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	command = "/car " + vehicleID
	item = menuItem(driver, "golden-card", now)
	item.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	messages := sender.Messages()
	menuText := "MAX Fleet\nДоступные автомобили\nМои поездки\nПравила и помощь"
	if messages[0].Text != menuText || !reflect.DeepEqual(messages[0].Buttons, [][]maxsdk.Button{{{Text: "Доступные автомобили", Payload: "cars:1"}}, {{Text: "Мои поездки", Payload: "trip-list:mine:1"}}}) {
		t.Fatalf("menu golden changed: %+v", messages[0])
	}
	catalogText := strings.Join([]string{
		"Доступные автомобили · страница 1",
		"DEMO-001 · Демо Учебный седан 1", "DEMO-002 · Демо Учебный седан 2",
		"DEMO-003 · Демо Учебный хэтчбек 1", "DEMO-004 · Демо Учебный хэтчбек 2",
		"DEMO-005 · Демо Учебный универсал 1", "Далее: /cars 2", "Обновить: /cars",
	}, "\n")
	if messages[1].Text != catalogText || len(messages[1].Buttons) != 6 || messages[1].Buttons[0][0].Payload != "car:"+vehicleID+":1" || !reflect.DeepEqual(messages[1].Buttons[5], []maxsdk.Button{{Text: "Обновить", Payload: "cars:1"}, {Text: "Далее", Payload: "cars:2"}}) {
		t.Fatalf("catalog golden changed: %+v", messages[1])
	}
	cardText := strings.Join([]string{
		"DEMO-001 · Демо Учебный седан 1", "Статус: available",
		"Место парковки: 55.750100, 37.620100", "Место подтверждено: 27.09.2026 12:00 MSK",
		"Топливо: 100%", "Топливо обновлено: 27.09.2026 12:00 MSK",
		"Пробег: 12000 км", "Пробег обновлён: 27.09.2026 12:00 MSK",
		"Описание: Синтетический автомобиль", "Известные замечания: Нет",
		"Ключи: Демо: ключ у ответственного", "К списку: /cars",
		"Выдача: доступно подтверждение оформления; поездка начнётся только после приёмки.",
	}, "\n")
	cardButtons := [][]maxsdk.Button{
		{{Text: "Начать оформление", Payload: "intent:" + vehicleID + ":1"}},
		{{Text: "Показать на карте", URL: "https://www.openstreetmap.org/?mlat=55.750100&mlon=37.620100#map=17/55.750100/37.620100"}},
		{{Text: "Предыдущий осмотр", Payload: "prev:" + vehicleID}},
		{{Text: "К списку", Payload: "cars:1"}},
	}
	if messages[2].Text != cardText || !reflect.DeepEqual(messages[2].Buttons, cardButtons) {
		t.Fatalf("card golden changed: %+v", messages[2])
	}
}

type fixedVehicleReader struct {
	emptyCatalogReader
	vehicle dataapi.Vehicle
}

func (r fixedVehicleReader) Vehicle(context.Context, string, string) (dataapi.Vehicle, error) {
	return r.vehicle, nil
}

func TestCardWithoutParkingOrKeysHidesCheckoutAction(t *testing.T) {
	const vehicleID = "10000000-0000-4000-8000-000000000001"
	ready := dataapi.Vehicle{ID: vehicleID, Status: "available", Version: 1, KeyInstructions: "У диспетчера", CurrentParking: &dataapi.ParkingLocation{Latitude: 55.75, Longitude: 37.62, ConfirmedAt: time.Now()}}
	for _, test := range []struct {
		name    string
		vehicle dataapi.Vehicle
		wantMap bool
	}{
		{name: "нет парковки", vehicle: func() dataapi.Vehicle { v := ready; v.CurrentParking = nil; return v }(), wantMap: false},
		{name: "нет ключей", vehicle: func() dataapi.Vehicle { v := ready; v.KeyInstructions = ""; return v }(), wantMap: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender := &maxsdk.RecordingTransport{}
			item := callbackItem("8000000000000000001", "no-checkout", "car:"+vehicleID+":1", time.Now())
			if err := (Bootstrap{Data: fixedVehicleReader{vehicle: test.vehicle}, MAX: sender}).Handle(context.Background(), item); err != nil {
				t.Fatal(err)
			}
			view := sender.Messages()[0]
			if !strings.Contains(view.Text, "Выдача: требуется") {
				t.Fatalf("missing requirement = %q", view.Text)
			}
			mapFound := false
			for _, row := range view.Buttons {
				for _, button := range row {
					if strings.HasPrefix(button.Payload, "intent:") {
						t.Fatalf("checkout offered without required data: %+v", view.Buttons)
					}
					mapFound = mapFound || button.URL != ""
				}
			}
			if mapFound != test.wantMap {
				t.Fatalf("map button = %t, want %t", mapFound, test.wantMap)
			}
		})
	}
}

func TestCheckoutMenuRestoresHoldAndCancelsOnlyAfterConfirmation(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicle := page.Items[0]
	if _, err := actor.CheckoutCreate(context.Background(), driver, vehicle.ID, vehicle.Version, "cancel-setup-hold", nil); err != nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Location: time.UTC}
	if err := processor.Handle(context.Background(), menuItem(driver, "hold-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[0]
	if !strings.Contains(menu.Text, "Hold до: 28.09.2026 09:15 UTC") || len(menu.Buttons) != 2 || !strings.HasPrefix(menu.Buttons[0][0].Payload, "math:") || !strings.HasPrefix(menu.Buttons[1][0].Payload, "cancel-intent:") {
		t.Fatalf("recovered hold menu = %+v", menu)
	}
	intent := callbackItem(driver, "cancel-intent", menu.Buttons[1][0].Payload, now)
	if err := processor.Handle(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	confirmation := sender.Messages()[1]
	if !strings.Contains(confirmation.Text, "Отменить оформление") || len(confirmation.Buttons) != 2 || !strings.HasPrefix(confirmation.Buttons[0][0].Payload, "cancel:") {
		t.Fatalf("cancel confirmation = %+v", confirmation)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("cancel intent removed hold: %+v %v", state, err)
	}
	confirm := callbackItem(driver, "cancel-confirm", confirmation.Buttons[0][0].Payload, now)
	if err := processor.Handle(context.Background(), confirm); err == nil {
		t.Fatal("accepted cancel without inbox lease")
	}
	if _, err := store.StoreInbox(context.Background(), confirm.Event, maxsdk.InboxIdempotencyKey(confirm.Event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "cancel-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	result, err := worker.RunOnce(context.Background(), 1)
	if err != nil || result.Acked != 1 {
		t.Fatalf("cancel worker = %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Trip != nil {
		t.Fatalf("cancel state = %+v %v", state, err)
	}
	if got := sender.Messages()[2].Text; !strings.Contains(got, "Машина освобождена") {
		t.Fatalf("cancel reply = %q", got)
	}
	page, err = actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 || page.Items[0].ID != vehicle.ID {
		t.Fatalf("released vehicle list = %+v %v", page, err)
	}
	if err := processor.Handle(context.Background(), confirm); err != nil || !strings.Contains(sender.Messages()[3].Text, "не найдено") {
		t.Fatalf("replayed cancel = %v, %+v", err, sender.Messages())
	}
	other := callbackItem("8000000000000000002", "other-cancel", confirmation.Buttons[0][0].Payload, now)
	if err := processor.Handle(context.Background(), other); err != nil || !strings.Contains(sender.Messages()[4].Text, "не найдено") {
		t.Fatalf("other actor cancel = %v, %+v", err, sender.Messages())
	}
}

func TestMathChallengeAttemptsStaleAnswerAndRecovery(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicle := page.Items[0]
	if _, err := actor.CheckoutCreate(context.Background(), driver, vehicle.ID, vehicle.Version, "math-setup-hold", nil); err != nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Location: time.UTC}
	worker := inboxworker.Worker{ID: "math-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if err := processor.Handle(context.Background(), menuItem(driver, "math-menu", now)); err != nil {
		t.Fatal(err)
	}
	deliver := func(key, payload string) maxsdk.RecordedText {
		t.Helper()
		event := callbackItem(driver, key, payload, now).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		result, err := worker.RunOnce(context.Background(), 1)
		if err != nil || result.Acked != 1 {
			t.Fatalf("math event %s = %+v %v", key, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1]
	}
	mathPayload := sender.Messages()[0].Buttons[0][0].Payload
	question := deliver("math-first", mathPayload)
	if len(question.Buttons) != 4 {
		t.Fatalf("math options = %+v", question)
	}
	var a, b int
	parts := strings.Split(question.Text, "\n")
	if len(parts) < 2 {
		t.Fatalf("math question = %q", question.Text)
	}
	if _, err := fmt.Sscanf(parts[1], "%d + %d = ?", &a, &b); err != nil {
		t.Fatalf("invalid math question = %q: %v", parts[1], err)
	}
	correctIndex := -1
	for i, row := range question.Buttons {
		value, err := strconv.Atoi(row[0].Text)
		if err != nil {
			t.Fatal(err)
		}
		if value == a+b {
			correctIndex = i
		}
	}
	if correctIndex < 0 {
		t.Fatalf("correct answer missing: %+v", question.Buttons)
	}
	wrongIndex := (correctIndex + 1) % 4
	stalePayload := question.Buttons[wrongIndex][0].Payload
	for attempt := 1; attempt <= 3; attempt++ {
		response := deliver(fmt.Sprintf("math-wrong-%d", attempt), question.Buttons[wrongIndex][0].Payload)
		if attempt < 3 {
			if !strings.Contains(response.Text, fmt.Sprintf("Осталось попыток: %d", 3-attempt)) || len(response.Buttons) != 4 {
				t.Fatalf("wrong attempt %d = %+v", attempt, response)
			}
			question = response
		} else if !strings.Contains(response.Text, "Три неверных ответа") || len(response.Buttons) != 0 {
			t.Fatalf("third wrong answer = %+v", response)
		}
	}
	stale := deliver("math-stale", stalePayload)
	if !strings.Contains(stale.Text, "устарел") {
		t.Fatalf("stale answer = %+v", stale)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Step != "math" || state.Trip != nil {
		t.Fatalf("math state after failures = %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "math-menu-again", now)); err != nil {
		t.Fatal(err)
	}
	newMenu := sender.Messages()[len(sender.Messages())-1]
	question = deliver("math-second", newMenu.Buttons[0][0].Payload)
	if len(question.Buttons) != 4 {
		t.Fatalf("new math question = %+v", question)
	}
	parts = strings.Split(question.Text, "\n")
	if _, err := fmt.Sscanf(parts[1], "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	for _, row := range question.Buttons {
		value, _ := strconv.Atoi(row[0].Text)
		if value == a+b {
			response := deliver("math-correct", row[0].Payload)
			if !strings.Contains(response.Text, "Ответ верный") {
				t.Fatalf("correct answer = %+v", response)
			}
			break
		}
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Step != "rules" || state.Checkout.IntentConfirmedAt == nil || state.Trip != nil {
		t.Fatalf("math state after correct answer = %+v %v", state, err)
	}
}
