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

func TestCheckoutFuelUsesVersionedDurableCommand(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := setupPhotoCheckout(t, actor, driver)
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "fuel-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[0]
	var fuelPayload string
	for _, row := range menu.Buttons {
		if strings.HasPrefix(row[0].Payload, "fuel:") {
			fuelPayload = row[0].Payload
		}
	}
	if fuelPayload != fmt.Sprintf("fuel:%s:%d", checkout.Inspection.ID, checkout.Inspection.Version) {
		t.Fatalf("fuel menu button = %q", fuelPayload)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "fuel-choose", fuelPayload, now)); err != nil {
		t.Fatal(err)
	}
	choices := sender.Messages()[1]
	if len(choices.Buttons) != 5 || choices.Buttons[0][0].Text != "0%" || choices.Buttons[3][0].Text != "75%" {
		t.Fatalf("fuel choices = %+v", choices.Buttons)
	}
	selected := choices.Buttons[3][0].Payload
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "other-fuel", selected, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменился") {
		t.Fatalf("other actor fuel: %v, %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "fuel-no-lease", selected, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("fuel without lease = %v", err)
	}
	invalid := strings.TrimSuffix(selected, ":75") + ":42"
	if err := processor.Handle(context.Background(), callbackItem(driver, "fuel-invalid", invalid, now)); err != nil || !strings.Contains(sender.Messages()[3].Text, "Некорректный") {
		t.Fatalf("invalid fuel: %v, %+v", err, sender.Messages())
	}
	event := callbackItem(driver, "fuel-save", selected, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "fuel-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[4].Text, "75% сохранено") {
		t.Fatalf("fuel save: %+v %v %+v", result, err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Inspection.FuelLevel == nil || *state.Checkout.Inspection.FuelLevel != 75 || state.Checkout.Inspection.Version != checkout.Inspection.Version+1 {
		t.Fatalf("saved fuel state: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "fuel-stale", selected, now)); err != nil || !strings.Contains(sender.Messages()[5].Text, "изменился") {
		t.Fatalf("stale fuel: %v, %+v", err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.Version != checkout.Inspection.Version+1 {
		t.Fatalf("stale fuel changed state: %+v %v", state, err)
	}
}

func TestFuelChoiceTargetRejectsMalformedPayload(t *testing.T) {
	for _, payload := range []string{"fuel-set:broken", "fuel-set:uuid:0:25", "fuel-set:uuid:x:25"} {
		item := callbackItem("8000000000000000001", "bad-fuel", payload, time.Now())
		_, _, _, recognized := fuelChoiceTarget(item.Event)
		if !recognized {
			t.Fatalf("payload %q was ignored", payload)
		}
	}
	if _, _, _, recognized := fuelChoiceTarget(dataapi.NormalizedEvent{}); recognized {
		t.Fatal("unrelated event recognized as fuel")
	}
}

func odometerTestItem(actor, key, value string, now time.Time) dataapi.InboxClaimItem {
	item := menuItem(actor, key, now)
	item.Event.Payload.Text = &value
	return item
}

func TestCheckoutOdometerRejectsBadAndRolledBackValues(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := setupPhotoCheckout(t, actor, driver)
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "odometer-menu", now)); err != nil {
		t.Fatal(err)
	}
	var prompt string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "odometer:") {
			prompt = row[0].Payload
		}
	}
	if prompt != fmt.Sprintf("odometer:%s:%d", checkout.Inspection.ID, checkout.Inspection.Version) {
		t.Fatalf("odometer menu = %q", prompt)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "odometer-prompt", prompt, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "/odometer 12000") {
		t.Fatalf("odometer prompt: %v %+v", err, sender.Messages())
	}
	for _, input := range []string{"/odometer -1", "/odometer 12.5", "/odometer abc", "/odometer 1 2", "/odometer 99999999999999999999999999"} {
		if err := processor.Handle(context.Background(), odometerTestItem(driver, "bad-"+input, input, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Укажите") && !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "диапазона") {
			t.Fatalf("invalid %q: %v %+v", input, err, sender.Messages())
		}
	}
	if err := processor.Handle(context.Background(), odometerTestItem(driver, "odo-no-lease", "/odometer 12001", now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("odometer without lease = %v", err)
	}
	worker := inboxworker.Worker{ID: "odometer-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	deliver := func(key, value string) string {
		t.Helper()
		event := odometerTestItem(driver, key, value, now).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
			t.Fatalf("odometer worker: %+v %v", result, err)
		}
		return sender.Messages()[len(sender.Messages())-1].Text
	}
	if got := deliver("odo-rollback", "/odometer 11999"); !strings.Contains(got, "меньше последнего") {
		t.Fatalf("rollback = %q", got)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.OdometerKM != nil || state.Checkout.Inspection.Version != checkout.Inspection.Version {
		t.Fatalf("rollback changed state: %+v %v", state, err)
	}
	if got := deliver("odo-valid", "/odometer 12001"); !strings.Contains(got, "12001 км сохранён") {
		t.Fatalf("valid odometer = %q", got)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.OdometerKM == nil || *state.Checkout.Inspection.OdometerKM != 12001 || state.Checkout.Inspection.Version != checkout.Inspection.Version+1 {
		t.Fatalf("saved odometer state: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "odo-stale", prompt, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменился") {
		t.Fatalf("stale odometer prompt: %v %+v", err, sender.Messages())
	}
}
