package dialog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type failDoneSave struct {
	*dataapi.Client
	fail bool
}

func (c *failDoneSave) ConversationSave(ctx context.Context, actor, employeeID string, version int64, input dataapi.ConversationSaveInput, key string, lease *dataapi.InboxLease) (dataapi.CommandResult, error) {
	if c.fail && input.Step == "done" {
		c.fail = false
		return dataapi.CommandResult{}, errors.New("synthetic interruption before done save")
	}
	return c.Client.ConversationSave(ctx, actor, employeeID, version, input, key, lease)
}

func TestBeforeIssueSubmitBlocksOnlyAfterSavedAndRecoversReply(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	damage := true
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &damage}, "submit-setup-damage", nil); err != nil {
		t.Fatal(err)
	}
	me, err := actor.Me(context.Background(), driver)
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	vehicle, err := actor.Vehicle(context.Background(), driver, checkout.VehicleID)
	if err != nil {
		t.Fatal(err)
	}
	assetIDs := []string{}
	for slot := 1; slot <= 2; slot++ {
		key := fmt.Sprintf("submit-stage-%d", slot)
		asset, err := actor.StageIssueAsset(context.Background(), driver, dataapi.IssueStageInput{ScopeType: "inspection", ScopeID: checkout.Inspection.ID, SourceEventKey: key, IdempotencyKey: key, ContentType: "image/png", Image: samplePhoto(t, uint8(80+slot))})
		if err != nil {
			t.Fatal(err)
		}
		assetIDs = append(assetIDs, asset.AssetID)
	}
	category, description, kind := "body_damage", "Царапина на крыле", "photo"
	if _, err := actor.ConversationSave(context.Background(), driver, me.Employee.ID, 1, dataapi.ConversationSaveInput{Flow: "issue_before", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &checkout.Inspection.ID, VehicleID: &checkout.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description, AssetIDs: assetIDs}}, "submit-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "submit-menu", now)); err != nil {
		t.Fatal(err)
	}
	var review string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "issue-review:") {
			review = row[0].Payload
		}
	}
	if review == "" {
		t.Fatal("issue review missing from menu")
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "submit-review", review, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "2/3") || !strings.Contains(sender.Messages()[1].Text, description) {
		t.Fatalf("review: %v %+v", err, sender.Messages())
	}
	payload := sender.Messages()[1].Buttons[0][0].Payload
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "foreign-submit", payload, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменился") {
		t.Fatalf("foreign submit: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "submit-no-lease", payload, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("submit without lease = %v", err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Conversation == nil || state.Conversation.Step != "collect_photos" {
		t.Fatalf("preview changed domain: %+v %v", state, err)
	}
	event := callbackItem(driver, "submit-issue", payload, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "submit-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(context.Background(), 1); err == nil && result.Acked != 0 {
		t.Fatalf("failed MAX response unexpectedly acked: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.IssueID == nil {
		t.Fatalf("lost reply lost issue: %+v %v", state, err)
	}
	issueID := *state.Conversation.Context.IssueID
	issue, err := actor.Issue(context.Background(), driver, issueID)
	if err != nil || issue.Stage != "before" || !issue.BlocksIssuance || issue.Description != description || len(issue.AssetIDs) != 2 || issue.AssetIDs[0] != assetIDs[0] || issue.AssetIDs[1] != assetIDs[1] {
		t.Fatalf("saved issue: %+v %v", issue, err)
	}
	vehicle, err = actor.Vehicle(context.Background(), driver, checkout.VehicleID)
	if err != nil || vehicle.Status != "unavailable" || !vehicle.NeedsReview {
		t.Fatalf("vehicle after issue: %+v %v", vehicle, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "submit-issue", payload, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Замечание сохранено") {
		t.Fatalf("reply recovery: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Conversation.Context.IssueID == nil || *state.Conversation.Context.IssueID != issueID {
		t.Fatalf("replay changed issue: %+v %v", state, err)
	}
}

func TestBeforeIssueSubmitRetriesAfterIssueCommitBeforeDone(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	damage := true
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &damage}, "retry-setup-damage", nil); err != nil {
		t.Fatal(err)
	}
	me, err := actor.Me(context.Background(), driver)
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	vehicle, err := actor.Vehicle(context.Background(), driver, checkout.VehicleID)
	if err != nil {
		t.Fatal(err)
	}
	category, description, kind := "other", "Синтетическая проблема", "photo"
	if _, err := actor.ConversationSave(context.Background(), driver, me.Employee.ID, 1, dataapi.ConversationSaveInput{Flow: "issue_before", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &checkout.Inspection.ID, VehicleID: &checkout.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description}}, "retry-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	command := &failDoneSave{Client: actor, fail: true}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: command, MAX: sender}
	payload := fmt.Sprintf("issue-submit:%s:2", checkout.Inspection.ID)
	event := callbackItem(driver, "retry-submit", payload, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "retry-submit-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("first interrupted submit: %+v %v", result, err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Conversation == nil || state.Conversation.Step != "collect_photos" {
		t.Fatalf("issue was not committed before done failure: %+v %v", state, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
		t.Fatalf("retried submit: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.IssueID == nil {
		t.Fatalf("retry did not mark issue done: %+v %v", state, err)
	}
	issue, err := actor.Issue(context.Background(), driver, *state.Conversation.Context.IssueID)
	if err != nil || issue.Stage != "before" || issue.Description != description || !issue.BlocksIssuance || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Замечание сохранено") {
		t.Fatalf("retried issue: %+v %v %+v", issue, err, sender.Messages())
	}
}
