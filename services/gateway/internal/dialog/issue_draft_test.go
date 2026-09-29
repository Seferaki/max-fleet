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

func TestIssueDraftCategoryDescriptionAndReplay(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	damage := true
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &damage}, "draft-setup-damage", nil); err != nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "draft-menu", now)); err != nil {
		t.Fatal(err)
	}
	var draftButton string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "issue-draft:") {
			draftButton = row[0].Payload
		}
	}
	if draftButton == "" {
		t.Fatal("new issue has no description entry")
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "draft-categories", draftButton, now)); err != nil {
		t.Fatal(err)
	}
	categories := sender.Messages()[1]
	if len(categories.Buttons) != 7 || categories.Buttons[0][0].Text != "Повреждение кузова" || categories.Buttons[4][0].Text != "Проблема с парковкой" || categories.Buttons[5][0].Text != "Машина не закрывается" {
		t.Fatalf("categories = %+v", categories.Buttons)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "draft-kind", categories.Buttons[0][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "/issue кузов") {
		t.Fatalf("category hint: %v %+v", err, sender.Messages())
	}
	for _, category := range []struct {
		index  int
		prompt string
	}{{4, "/issue парковка"}, {5, "/issue замок"}} {
		id := fmt.Sprintf("draft-kind-%d", category.index)
		if err := processor.Handle(context.Background(), callbackItem(driver, id, categories.Buttons[category.index][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, category.prompt) {
			t.Fatalf("category prompt %d: %v %+v", category.index, err, sender.Messages())
		}
	}
	for _, bad := range []string{"/issue", "/issue неверно текст"} {
		if err := processor.Handle(context.Background(), odometerTestItem(driver, "bad-draft-"+bad, bad, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Категории") && !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "неверны") {
			t.Fatalf("invalid draft %q: %v %+v", bad, err, sender.Messages())
		}
	}
	const command = "/issue кузов Царапина на левом крыле"
	if err := processor.Handle(context.Background(), odometerTestItem(driver, "draft-no-lease", command, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("draft without lease = %v", err)
	}
	event := odometerTestItem(driver, "draft-save", command, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "draft-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Описание сохранено") {
		t.Fatalf("draft save: %+v %v %+v", result, err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Conversation == nil || state.ConversationVersion != 2 || state.Conversation.Flow != "issue_before" || state.Conversation.Context.IssueCategory == nil || *state.Conversation.Context.IssueCategory != "body_damage" || state.Conversation.Context.DraftText == nil || *state.Conversation.Context.DraftText != "Царапина на левом крыле" || state.Conversation.Context.TargetID == nil || *state.Conversation.Context.TargetID != checkout.Inspection.ID {
		t.Fatalf("saved draft: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), odometerTestItem(driver, "draft-replay", command, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("same draft replay: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.ConversationVersion != 2 {
		t.Fatalf("replay changed draft version: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "stale-draft", fmt.Sprintf("issue-kind:%s:%d:body_damage", checkout.Inspection.ID, checkout.Inspection.Version), now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменился") {
		t.Fatalf("stale category: %v %+v", err, sender.Messages())
	}
}
