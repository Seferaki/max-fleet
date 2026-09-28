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

func TestCheckoutSummaryStartAndLostReplyRecovery(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "before-start-no-answer", now)); err != nil {
		t.Fatal(err)
	}
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "checkout-summary:") {
			t.Fatal("summary offered before no-new-issues confirmation")
		}
	}
	noDamage := false
	updated, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "start-setup-answer", nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := dataapi.DecodeAggregate[dataapi.Inspection](updated)
	if err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("state after answer: %+v %v", state, err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(context.Background(), driver, checkout.ID, state.Checkout.Version, "start-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || !readyForCheckoutStart(*state.Checkout) || state.Checkout.Inspection.Version != inspection.Version {
		t.Fatalf("ready state: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "before-start-ready", now)); err != nil {
		t.Fatal(err)
	}
	var summary string
	for _, row := range sender.Messages()[1].Buttons {
		if strings.HasPrefix(row[0].Payload, "checkout-summary:") {
			summary = row[0].Payload
		}
	}
	if summary != fmt.Sprintf("checkout-summary:%s:%d", checkout.ID, state.Checkout.Version) {
		t.Fatalf("summary button = %q", summary)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "show-start-summary", summary, now)); err != nil {
		t.Fatal(err)
	}
	preview := sender.Messages()[2]
	for _, part := range []string{"8/8", "50%", "12001 км", "Новых замечаний нет", "Правила приняты", "Hold до:", "подтверждаю"} {
		if !strings.Contains(preview.Text, part) {
			t.Fatalf("summary missing %q: %q", part, preview.Text)
		}
	}
	start := preview.Buttons[0][0].Payload
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "foreign-start", start, now)); err != nil || !strings.Contains(sender.Messages()[3].Text, "изменилось") {
		t.Fatalf("foreign start: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "start-without-lease", start, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("start without lease = %v", err)
	}
	stale := fmt.Sprintf("checkout-start:%s:%d", checkout.ID, state.Checkout.Version-1)
	if err := processor.Handle(context.Background(), callbackItem(driver, "stale-start", stale, now)); err != nil || !strings.Contains(sender.Messages()[4].Text, "изменилось") {
		t.Fatalf("stale start: %v %+v", err, sender.Messages())
	}
	event := callbackItem(driver, "begin-trip", start, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "start-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost reply: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Trip == nil || state.Trip.Status != "active" || state.Trip.CheckoutID != checkout.ID || state.Trip.BeforeInspection.Status != "finalized" || len(state.Trip.BeforeInspection.OccupiedSlots) != 8 {
		t.Fatalf("committed trip: %+v %v", state, err)
	}
	tripID := state.Trip.ID
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Поездка началась") {
		t.Fatalf("recovered reply: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Trip.ID != tripID {
		t.Fatalf("replay changed trip: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "active-menu", now)); err != nil {
		t.Fatal(err)
	}
	var active bool
	for _, row := range sender.Messages()[len(sender.Messages())-1].Buttons {
		active = active || row[0].Payload == "trip:"+tripID
	}
	if !active {
		t.Fatal("active trip card missing from menu")
	}
}

func TestCheckoutStartRejectsExpiredHold(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, _, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "expired-start-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("before expiry: %+v %v", state, err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(context.Background(), driver, checkout.ID, state.Checkout.Version, "expired-start-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready before expiry: %+v %v", state, err)
	}
	payload := fmt.Sprintf("checkout-start:%s:%d", checkout.ID, state.Checkout.Version)
	current = now.Add(16 * time.Minute)
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), callbackItem(driver, "expired-start", payload, current)); err != nil || !strings.Contains(sender.Messages()[0].Text, "истёк") {
		t.Fatalf("expired start response: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip != nil || state.Checkout != nil {
		t.Fatalf("expired hold started trip: %+v %v", state, err)
	}
}
