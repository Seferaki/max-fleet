package dialog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestFullTakeThroughDialogOnMock(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	sender := &maxsdk.RecordingTransport{}
	fetcher := &syntheticPhotoFetcher{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Photos: fetcher, PhotoStore: actor, Location: time.UTC}
	worker := inboxworker.Worker{ID: "full-take-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
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
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
			t.Fatalf("event %s: %+v %v", event.EventKey, result, err)
		}
	}
	callback := func(key, payload string) {
		t.Helper()
		deliver(callbackItem(driver, key, payload, now).Event)
	}
	menu := func(key string) {
		t.Helper()
		if err := processor.Handle(context.Background(), menuItem(driver, key, now)); err != nil {
			t.Fatal(err)
		}
	}
	menu("take-menu")
	callback("take-cars", button("cars:"))
	callback("take-car", button("car:"))
	callback("take-intent", button("intent:"))
	if !strings.Contains(latest().Text, "15 минут") {
		t.Fatalf("hold preview: %+v", latest())
	}
	callback("take-confirm", button("take:"))
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || !state.Checkout.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("15 minute hold: %+v %v", state, err)
	}
	menu("take-math-menu")
	callback("take-math", button("math:"))
	question := latest()
	var a, b int
	for _, line := range strings.Split(question.Text, "\n") {
		if _, err := fmt.Sscanf(line, "%d + %d = ?", &a, &b); err == nil {
			break
		}
	}
	var answer string
	for _, row := range question.Buttons {
		value, parseErr := strconv.Atoi(row[0].Text)
		if parseErr == nil && value == a+b {
			answer = row[0].Payload
		}
	}
	if answer == "" {
		t.Fatalf("math challenge has no answer: %+v", question)
	}
	callback("take-answer", answer)
	menu("take-rules-menu")
	callback("take-rules", button("rules:"))
	callback("take-accept-rules", button("accept-rules:"))
	for slot := 1; slot <= 8; slot++ {
		fetcher.image = samplePhoto(t, uint8(slot))
		deliver(photoItem(driver, fmt.Sprintf("take-photo-%d", slot), now).Event)
		if !strings.Contains(latest().Text, fmt.Sprintf("%d/8", slot)) {
			t.Fatalf("photo %d: %+v", slot, latest())
		}
	}
	menu("take-photo-menu")
	callback("take-confirm-photos", button("confirm-photos:"))
	menu("take-fuel-menu")
	callback("take-fuel", button("fuel:"))
	callback("take-set-fuel", button("fuel-set:"))
	text := "/odometer 12001"
	input := menuItem(driver, "take-odometer", now).Event
	input.Payload.Text = &text
	deliver(input)
	menu("take-issues-menu")
	callback("take-issue-question", button("new-issues:"))
	callback("take-no-issues", button("new-issues-set:"))
	menu("take-summary-menu")
	callback("take-summary", button("checkout-summary:"))
	if !strings.Contains(latest().Text, "8/8") || !strings.Contains(latest().Text, "подтверждаю") {
		t.Fatalf("final summary: %+v", latest())
	}
	callback("take-start", button("checkout-start:"))
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Trip == nil || state.Trip.Status != "active" || state.Trip.BeforeInspection.Status != "finalized" || len(state.Trip.BeforeInspection.OccupiedSlots) != 8 {
		t.Fatalf("final trip: %+v %v", state, err)
	}
	vehicle, err := actor.Vehicle(context.Background(), driver, state.Trip.VehicleID)
	if err != nil || vehicle.Status != "in_trip" || vehicle.CurrentOdometerKM == nil || *vehicle.CurrentOdometerKM != 12001 {
		t.Fatalf("vehicle after start: %+v %v", vehicle, err)
	}
}
