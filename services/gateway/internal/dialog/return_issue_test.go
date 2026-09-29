package dialog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestReturnIssueDraftOwnedReturnAndDurableSave(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	readyChecklistDraft(t, actor, driver)
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Trip == nil {
		t.Fatalf("return state: %+v %v", state, err)
	}
	draftReturn := *state.Return
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(ctx, menuItem(driver, "after-issue-menu", now)); err != nil {
		t.Fatal(err)
	}
	button := fmt.Sprintf("return-issue:%s:%d", draftReturn.ID, draftReturn.Version)
	found := false
	for _, row := range sender.Messages()[0].Buttons {
		if row[0].Payload == button {
			found = true
		}
	}
	if !found {
		t.Fatalf("return menu has no issue entry: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "after-issue-foreign", button, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "изменился") {
		t.Fatalf("foreign return issue: %v %+v", err, sender.Messages())
	}
	stale := fmt.Sprintf("return-issue:%s:%d", draftReturn.ID, draftReturn.Version+1)
	if err := processor.Handle(ctx, callbackItem(driver, "after-issue-stale", stale, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменился") {
		t.Fatalf("stale return issue: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "after-issue-categories", button, now)); err != nil || len(sender.Messages()[3].Buttons) != 5 {
		t.Fatalf("categories: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "after-issue-kind", sender.Messages()[3].Buttons[2][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[4].Text, "/issue чистота") {
		t.Fatalf("category hint: %v %+v", err, sender.Messages())
	}
	const command = "/issue чистота Грязный салон после поездки"
	if err := processor.Handle(ctx, odometerTestItem(driver, "after-issue-no-lease", command, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("draft without lease: %v", err)
	}
	event := odometerTestItem(driver, "after-issue-save", command, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "after-issue-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "ещё не отправлено") {
		t.Fatalf("draft save: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Flow != "issue_after" || state.ConversationVersion != 2 || state.Conversation.Context.ReturnID == nil || *state.Conversation.Context.ReturnID != draftReturn.ID || state.Conversation.Context.TargetID == nil || *state.Conversation.Context.TargetID != draftReturn.Inspection.ID || state.Return == nil || state.Return.Status != "draft" {
		t.Fatalf("saved after draft: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, odometerTestItem(driver, "after-issue-repeat", command, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("repeat draft: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.ConversationVersion != 2 {
		t.Fatalf("repeat changed draft: %+v %v", state, err)
	}
}
