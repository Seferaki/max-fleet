package dialog

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
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
	t.Helper()
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", filepath.Join(t.TempDir(), "snapshot.json"), func() time.Time { return now })
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

func TestBootstrapWorkerAcksMenuButKeepsFollowingPhoto(t *testing.T) {
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
	if err != nil || second.Deferred != 1 || second.Acked != 0 || len(sender.Messages()) != 1 {
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
func (emptyCatalogReader) Vehicles(context.Context, string, dataapi.VehicleFilter) (dataapi.Page[dataapi.Vehicle], error) {
	return dataapi.Page[dataapi.Vehicle]{}, nil
}
func (emptyCatalogReader) Vehicle(context.Context, string, string) (dataapi.Vehicle, error) {
	return dataapi.Vehicle{}, errors.New("unexpected vehicle read")
}
func (emptyCatalogReader) PreviousInspection(context.Context, string, string) (dataapi.Inspection, error) {
	return dataapi.Inspection{}, errors.New("unexpected previous inspection read")
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

func TestCardShowsMissingFieldsAsUnknown(t *testing.T) {
	vehicle := dataapi.Vehicle{Plate: "DEMO-NEW", Status: "unavailable"}
	card := cardText(vehicle, time.UTC)
	for _, required := range []string{"Место парковки: Не указано", "Топливо: Не указано", "Пробег: Не указано", "Описание: Не указано", "Ключи: Не указано"} {
		if !strings.Contains(card, required) {
			t.Fatalf("card misses %q: %q", required, card)
		}
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
	if len(menu.Buttons) != 1 || menu.Buttons[0][0].Payload != "cars:1" {
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
	if !strings.Contains(card.Text, "Данные автомобиля изменились") || !strings.Contains(card.Text, "Статус:") || len(card.Buttons) != 2 || card.Buttons[0][0].Payload != "prev:"+vehicle.ID || card.Buttons[1][0].Payload != "cars:1" {
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
