package dialog

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
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
