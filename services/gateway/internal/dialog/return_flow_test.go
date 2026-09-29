package dialog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestFullReturnThroughDialogOnMock(t *testing.T) {
	now := time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	draft := readyChecklistDraft(t, actor, driver)
	sender := &maxsdk.RecordingTransport{}
	fetcher := &syntheticPhotoFetcher{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Photos: fetcher, PhotoStore: actor, Location: time.UTC}
	worker := inboxworker.Worker{ID: "full-return-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	latest := func() maxsdk.RecordedText {
		messages := sender.Messages()
		return messages[len(messages)-1]
	}
	button := func(prefix string) string {
		t.Helper()
		for _, row := range latest().Buttons {
			for _, choice := range row {
				if strings.HasPrefix(choice.Payload, prefix) {
					return choice.Payload
				}
			}
		}
		t.Fatalf("button %q missing in %+v", prefix, latest())
		return ""
	}
	deliver := func(event dataapi.NormalizedEvent) {
		t.Helper()
		if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
			t.Fatalf("event %s: %+v %v", event.EventKey, result, err)
		}
	}
	callback := func(key, payload string) {
		t.Helper()
		deliver(callbackItem(driver, key, payload, now).Event)
	}
	menu := func(key string) {
		t.Helper()
		if err := processor.Handle(ctx, menuItem(driver, key, now)); err != nil {
			t.Fatal(err)
		}
	}

	for index, field := range []string{"damage", "clean", "parking", "keys", "locked"} {
		menu(fmt.Sprintf("return-flow-check-menu-%d", index))
		callback(fmt.Sprintf("return-flow-check-open-%d", index), button("return-check:"))
		answer := latest().Buttons[0][0].Payload
		if field == "damage" {
			answer = latest().Buttons[1][0].Payload
		}
		callback(fmt.Sprintf("return-flow-check-save-%d", index), answer)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil || nextReturnCheckField(state.Return.Inspection) != "" {
		t.Fatalf("checklist incomplete: %+v %v", state, err)
	}
	menu("return-flow-photo-menu")
	callback("return-flow-photo-open", button("return-photos:"))
	for slot := 1; slot <= 8; slot++ {
		fetcher.image = samplePhoto(t, uint8(150+slot))
		deliver(photoItem(driver, fmt.Sprintf("return-flow-photo-%d", slot), now).Event)
		if !strings.Contains(latest().Text, fmt.Sprintf("%d/8", slot)) {
			t.Fatalf("after photo %d: %+v", slot, latest())
		}
	}
	menu("return-flow-photo-confirm-menu")
	callback("return-flow-photo-confirm", button("return-confirm-photos:"))
	menu("return-flow-fuel-menu")
	callback("return-flow-fuel-open", button("return-fuel:"))
	callback("return-flow-fuel-save", latest().Buttons[3][0].Payload)
	menu("return-flow-odometer-menu")
	callback("return-flow-odometer-open", button("return-odometer:"))
	odometer := "/odometer 12050"
	item := menuItem(driver, "return-flow-odometer-save", now).Event
	item.Payload.Text = &odometer
	deliver(item)
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Return.ParkingLocation != nil {
		t.Fatalf("unexpected location before map selection: %+v %v", state, err)
	}
	if _, err := actor.ReturnSetLocation(ctx, driver, draft.ID, state.Return.Version, "return-flow-manual-map", nil, dataapi.LocationInput{Latitude: 55.75, Longitude: 37.62, Source: "manual_map", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	menu("return-flow-summary-menu")
	callback("return-flow-summary", button("return-summary:"))
	if !strings.Contains(latest().Text, "Фото после: 8/8") || !strings.Contains(latest().Text, "manual_map") {
		t.Fatalf("return summary: %+v", latest())
	}
	callback("return-flow-complete", button("return-complete:"))
	if !strings.Contains(latest().Text, "Возврат подтверждён") {
		t.Fatalf("unconfirmed return: %+v", latest())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return != nil || state.Trip != nil {
		t.Fatalf("completed return remains active: %+v %v", state, err)
	}
	trip, err := actor.Trip(ctx, driver, draft.TripID)
	if err != nil || trip.Status != "completed" || trip.AfterInspection == nil || len(trip.AfterInspection.OccupiedSlots) != 8 || len(trip.BeforeInspection.OccupiedSlots) != 8 || trip.ParkingLocation == nil || trip.ParkingLocation.Source != "manual_map" {
		t.Fatalf("completed trip missing 8+8 photos or parking: %+v %v", trip, err)
	}
	vehicle, err := actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("vehicle was not released after confirmed complete: %+v %v", vehicle, err)
	}
}
