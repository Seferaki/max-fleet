package dialog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func geoTestItem(actor, key string, lat, lon float64, now time.Time) dataapi.InboxClaimItem {
	return dataapi.InboxClaimItem{ID: key, Event: dataapi.NormalizedEvent{
		IntegrationKey: "demo-bot", EventKey: "message:" + key + ":message_created", EventType: "message_created",
		ActorMaxUserID: actor, ChatID: actor, MessageID: &key, OccurredAt: now,
		Payload: dataapi.NormalizedPayload{Kind: "geo", Latitude: &lat, Longitude: &lon, AttachmentCount: 1},
	}}
}

func TestReturnMAXGeoNeedsExplicitConfirmAndRecoversCommit(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	readyChecklistDraft(t, actor, driver)
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	command := &failDoneSave{Client: actor, fail: true}
	processor := Bootstrap{Data: actor, Commands: command, MAX: sender}
	geo := geoTestItem(driver, "geo-share", 55.75, 37.62, now)
	if err := processor.Handle(ctx, geo); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("geo without lease: %v", err)
	}
	if _, err := store.StoreInbox(ctx, geo.Event, maxsdk.InboxIdempotencyKey(geo.Event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "geo-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[0].Text, "не сохраняет место") {
		t.Fatalf("geo preview: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Return.ParkingLocation != nil || state.Conversation == nil || state.Conversation.Step != "confirm" {
		t.Fatalf("geo silently saved location: %+v %v", state, err)
	}
	confirm := sender.Messages()[0].Buttons[0][0].Payload
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "geo-foreign", confirm, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "устарело") {
		t.Fatalf("foreign confirmation: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "geo-no-lease", confirm, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("confirm without lease: %v", err)
	}
	event := callbackItem(driver, "geo-confirm", confirm, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("interrupted after location commit: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Return.ParkingLocation == nil || state.Return.ParkingLocation.Source != "max_geo" || state.Conversation == nil || state.Conversation.Step != "confirm" {
		t.Fatalf("domain location before conversation done: %+v %v", state, err)
	}
	parkingID := state.Return.ParkingLocation.ID
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
		t.Fatalf("retry confirmation: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Return == nil || state.Return.ParkingLocation == nil || state.Return.ParkingLocation.ID != parkingID {
		t.Fatalf("retry created another location: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, callbackItem(driver, "geo-confirm", confirm, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже подтверждена") {
		t.Fatalf("lost reply recovery: %v %+v", err, sender.Messages())
	}
}
