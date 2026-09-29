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

func TestTripIssueDraftOwnerVersionAndDurableSave(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(ctx, driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "trip-issue-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(ctx, driver, checkout.ID, state.Checkout.Version, "trip-issue-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutStart(ctx, driver, checkout.ID, state.Checkout.Version, "trip-issue-setup-start", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" {
		t.Fatalf("active trip: %+v %v", state, err)
	}
	trip := *state.Trip
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-card", "trip:"+trip.ID, now)); err != nil {
		t.Fatal(err)
	}
	button := fmt.Sprintf("trip-issue:%s:%d", trip.ID, trip.Version)
	found := false
	for _, row := range sender.Messages()[0].Buttons {
		if row[0].Payload == button {
			found = true
		}
	}
	if !found {
		t.Fatalf("trip card has no issue entry: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "trip-issue-foreign", button, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "недоступна") {
		t.Fatalf("foreign issue entry: %v %+v", err, sender.Messages())
	}
	stale := fmt.Sprintf("trip-issue:%s:%d", trip.ID, trip.Version+1)
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-stale", stale, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменилась") {
		t.Fatalf("stale issue entry: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-categories", button, now)); err != nil || len(sender.Messages()[3].Buttons) != 5 {
		t.Fatalf("categories: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-kind", sender.Messages()[3].Buttons[1][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[4].Text, "/issue механика") {
		t.Fatalf("category hint: %v %+v", err, sender.Messages())
	}
	const command = "/issue механика Двигатель шумит при движении"
	if err := processor.Handle(ctx, odometerTestItem(driver, "trip-issue-no-lease", command, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("draft without lease: %v", err)
	}
	event := odometerTestItem(driver, "trip-issue-save", command, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "trip-issue-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "ещё не отправлено") {
		t.Fatalf("draft save: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Flow != "issue_during" || state.ConversationVersion != 2 || state.Conversation.Context.TripID == nil || *state.Conversation.Context.TripID != trip.ID || state.Trip == nil || state.Trip.Status != "active" {
		t.Fatalf("saved draft: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, odometerTestItem(driver, "trip-issue-repeat", command, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("repeat: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.ConversationVersion != 2 {
		t.Fatalf("repeat changed draft: %+v %v", state, err)
	}
}
