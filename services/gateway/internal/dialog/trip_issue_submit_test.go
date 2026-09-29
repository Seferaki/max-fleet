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

func TestTripIssueSubmitNotifiesAfterCommitAndRecovers(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(ctx, driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "trip-submit-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatal(err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(ctx, driver, checkout.ID, state.Checkout.Version, "trip-submit-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatal(err)
	}
	if _, err := actor.CheckoutStart(ctx, driver, checkout.ID, state.Checkout.Version, "trip-submit-setup-start", nil); err != nil {
		t.Fatal(err)
	}
	started, err := store.ClaimNotifications(ctx, "trip-submit-setup-notifier", 1, "trip-submit-start-claim")
	if err != nil || len(started.Items) != 1 || started.Items[0].Event.Type != "trip_started" {
		t.Fatalf("start outbox: %+v %v", started, err)
	}
	if _, err := store.AckNotification(ctx, started.Items[0].DeliveryID, started.Items[0].LeaseToken, "synthetic-start-receipt", "trip-submit-start-ack"); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil {
		t.Fatal(err)
	}
	trip := *state.Trip
	me, err := actor.Me(ctx, driver)
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	vehicle, err := actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := actor.StageIssueAsset(ctx, driver, dataapi.IssueStageInput{ScopeType: "trip", ScopeID: trip.ID, SourceEventKey: "trip-submit-photo", IdempotencyKey: "trip-submit-stage", ContentType: "image/png", Image: samplePhoto(t, 70)})
	if err != nil {
		t.Fatal(err)
	}
	category, description, kind := "mechanical", "Синтетическая неисправность двигателя", "photo"
	input := dataapi.ConversationSaveInput{Flow: "issue_during", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &trip.ID, TripID: &trip.ID, VehicleID: &trip.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description, AssetIDs: []string{asset.AssetID}}}
	if _, err := actor.ConversationSave(ctx, driver, me.Employee.ID, 1, input, "trip-submit-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	command := &failDoneSave{Client: actor, fail: true}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: command, MAX: sender}
	if err := processor.Handle(ctx, menuItem(driver, "trip-submit-menu", now)); err != nil {
		t.Fatal(err)
	}
	var review string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "trip-issue-review:") {
			review = row[0].Payload
		}
	}
	if review == "" {
		t.Fatal("issue review missing from active trip menu")
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-submit-review", review, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, description) || !strings.Contains(sender.Messages()[1].Text, "1/3") {
		t.Fatalf("review: %v %+v", err, sender.Messages())
	}
	submit := sender.Messages()[1].Buttons[0][0].Payload
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "trip-submit-foreign", submit, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменились") {
		t.Fatalf("foreign submit: %v %+v", err, sender.Messages())
	}
	stale := fmt.Sprintf("trip-issue-submit:%s:%d", trip.ID, 3)
	if err := processor.Handle(ctx, callbackItem(driver, "trip-submit-stale", stale, now)); err != nil || !strings.Contains(sender.Messages()[3].Text, "изменились") {
		t.Fatalf("stale submit: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-submit-no-lease", submit, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("submit without lease: %v", err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil || len(state.Trip.Issues) != 0 {
		t.Fatalf("review mutated trip: %+v %v", state, err)
	}
	event := callbackItem(driver, "trip-submit-issue", submit, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "trip-submit-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("interrupted after issue commit: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" || len(state.Trip.Issues) != 1 || state.Conversation == nil || state.Conversation.Step != "collect_photos" {
		t.Fatalf("issue commit state: %+v %v", state, err)
	}
	issueID := state.Trip.Issues[0].ID
	vehicle, err = actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil || vehicle.Status != "in_trip" || !vehicle.NeedsReview {
		t.Fatalf("vehicle after issue: %+v %v", vehicle, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
		t.Fatalf("retried submit: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.IssueID == nil || *state.Conversation.Context.IssueID != issueID || len(state.Trip.Issues) != 1 {
		t.Fatalf("completed submit: %+v %v", state, err)
	}
	claim, err := store.ClaimNotifications(ctx, "trip-submit-notifier", 10, "trip-submit-claim")
	if err != nil || len(claim.Items) != 1 || claim.Items[0].Event.Type != "issue_created" || claim.Items[0].Event.ResourceID != issueID {
		t.Fatalf("admin outbox: %+v %v", claim, err)
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-submit-issue", submit, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Замечание во время поездки сохранено") {
		t.Fatalf("recovered reply: %v %+v", err, sender.Messages())
	}
}
