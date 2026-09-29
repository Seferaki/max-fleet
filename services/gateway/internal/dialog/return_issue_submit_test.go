package dialog

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestReturnIssuesRequireSeparateDamageAndDirtyReportsWithRetry(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	readyChecklistDraft(t, actor, driver)
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Trip == nil {
		t.Fatal(err)
	}
	tripID, returnID, inspectionID := state.Trip.ID, state.Return.ID, state.Return.Inspection.ID
	damage, clean := true, false
	if _, err := actor.InspectionUpdate(ctx, driver, inspectionID, state.Return.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &damage, CabinClean: &clean}, "after-submit-setup-checks", nil); err != nil {
		t.Fatal(err)
	}
	started, err := store.ClaimNotifications(ctx, "after-submit-setup-notifier", 1, "after-submit-start-claim")
	if err != nil || len(started.Items) != 1 || started.Items[0].Event.Type != "trip_started" {
		t.Fatalf("start notification: %+v %v", started, err)
	}
	if _, err := store.AckNotification(ctx, started.Items[0].DeliveryID, started.Items[0].LeaseToken, "synthetic-start-receipt", "after-submit-start-ack"); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil {
		t.Fatal(err)
	}
	me, err := actor.Me(ctx, driver)
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	vehicle, err := actor.Vehicle(ctx, driver, state.Trip.VehicleID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := actor.StageIssueAsset(ctx, driver, dataapi.IssueStageInput{ScopeType: "inspection", ScopeID: inspectionID, SourceEventKey: "after-submit-photo", IdempotencyKey: "after-submit-stage", ContentType: "image/png", Image: samplePhoto(t, 101)})
	if err != nil {
		t.Fatal(err)
	}
	category, description, kind := "body_damage", "Новая царапина", "photo"
	input := dataapi.ConversationSaveInput{Flow: "issue_after", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &inspectionID, TripID: &tripID, ReturnID: &returnID, VehicleID: &state.Trip.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description, AssetIDs: []string{asset.AssetID}}}
	if _, err := actor.ConversationSave(ctx, driver, me.Employee.ID, 1, input, "after-submit-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	command := &failDoneSave{Client: actor, fail: true}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: command, MAX: sender}
	if err := processor.Handle(ctx, menuItem(driver, "after-submit-menu", now)); err != nil {
		t.Fatal(err)
	}
	var review string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-issue-review:") {
			review = row[0].Payload
		}
	}
	if review == "" {
		t.Fatal("after issue review missing")
	}
	if err := processor.Handle(ctx, callbackItem(driver, "after-submit-review", review, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, description) || !strings.Contains(sender.Messages()[1].Text, "1/3") {
		t.Fatalf("review: %v %+v", err, sender.Messages())
	}
	submit := sender.Messages()[1].Buttons[0][0].Payload
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "after-submit-foreign", submit, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменились") {
		t.Fatalf("foreign submit: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "after-submit-no-lease", submit, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("submit without lease: %v", err)
	}
	event := callbackItem(driver, "after-submit-damage", submit, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "after-submit-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("interrupted after issue commit: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Trip == nil || state.Trip.Status != "returning" || len(state.Trip.Issues) != 1 || state.Conversation == nil || state.Conversation.Step != "collect_photos" {
		t.Fatalf("first issue committed: %+v %v", state, err)
	}
	firstIssueID := state.Trip.Issues[0].ID
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "загрязнение") {
		t.Fatalf("retry and second issue prompt: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || len(state.Trip.Issues) != 1 || state.Trip.Issues[0].ID != firstIssueID {
		t.Fatalf("first issue duplicated: %+v %v", state, err)
	}
	firstClaim, err := store.ClaimNotifications(ctx, "after-submit-notifier", 10, "after-submit-first-claim")
	if err != nil || len(firstClaim.Items) != 1 || firstClaim.Items[0].Event.Type != "issue_created" || firstClaim.Items[0].Event.ResourceID != firstIssueID {
		t.Fatalf("first issue outbox: %+v %v", firstClaim, err)
	}
	if _, err := store.AckNotification(ctx, firstClaim.Items[0].DeliveryID, firstClaim.Items[0].LeaseToken, "synthetic-first-receipt", "after-submit-first-ack"); err != nil {
		t.Fatal(err)
	}
	secondEvent := odometerTestItem(driver, "after-submit-dirty-draft", "/issue чистота Грязный салон", current).Event
	if _, err := store.StoreInbox(ctx, secondEvent, maxsdk.InboxIdempotencyKey(secondEvent)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
		t.Fatalf("second draft: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "collect_photos" || state.Conversation.Context.IssueCategory == nil || *state.Conversation.Context.IssueCategory != "cleanliness" || len(state.Conversation.Context.AssetIDs) != 0 {
		t.Fatalf("second issue inherited first assets: %+v %v", state, err)
	}
	secondSubmit := "return-issue-submit:" + inspectionID + ":" + strconv.FormatInt(state.Conversation.Version, 10)
	second := callbackItem(driver, "after-submit-dirty", secondSubmit, current).Event
	if _, err := store.StoreInbox(ctx, second, maxsdk.InboxIdempotencyKey(second)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
		t.Fatalf("second submit: %+v %v", result, err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil || len(state.Trip.Issues) != 2 || state.Conversation == nil || state.Conversation.Step != "done" || len(missingAfterIssueReports(state.Return.Inspection, state.Trip.Issues)) != 0 {
		t.Fatalf("both issues: %+v %v", state, err)
	}
	secondClaim, err := store.ClaimNotifications(ctx, "after-submit-notifier", 10, "after-submit-second-claim")
	if err != nil || len(secondClaim.Items) != 1 || secondClaim.Items[0].Event.Type != "issue_created" || secondClaim.Items[0].Event.ResourceID != state.Trip.Issues[1].ID {
		t.Fatalf("second issue outbox: %+v %v", secondClaim, err)
	}
}
