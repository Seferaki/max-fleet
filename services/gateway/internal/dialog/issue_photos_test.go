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

func TestIssuePhotoStageKeepsEightSlotsAndCapsThree(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	damage := true
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &damage}, "issue-photo-setup-damage", nil); err != nil {
		t.Fatal(err)
	}
	me, err := actor.Me(context.Background(), driver)
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	category, description, kind := "body_damage", "Синтетическая царапина", "photo"
	version := int64(2)
	if _, err := actor.ConversationSave(context.Background(), driver, me.Employee.ID, 1, dataapi.ConversationSaveInput{Flow: "issue_before", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &checkout.Inspection.ID, VehicleID: &checkout.VehicleID, VehicleVersion: &version, IssueCategory: &category, DraftText: &description}}, "issue-photo-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	fetcher := &syntheticPhotoFetcher{image: samplePhoto(t, 40)}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, PhotoStore: actor, Photos: fetcher, MAX: sender}
	worker := inboxworker.Worker{ID: "issue-photo-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	deliver := func(maxID, key string) string {
		t.Helper()
		event := photoItem(maxID, key, now).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
			t.Fatalf("photo %s: %+v %v", key, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1].Text
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "issue-photo-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[0]
	if !strings.Contains(menu.Text, "0/3") {
		t.Fatalf("issue photo progress missing: %+v", menu)
	}
	var help string
	for _, row := range menu.Buttons {
		if strings.HasPrefix(row[0].Payload, "issue-photos:") {
			help = row[0].Payload
		}
	}
	if help == "" {
		t.Fatal("no issue photo help button")
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "issue-photo-help", help, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "0/3") {
		t.Fatalf("help = %v %+v", err, sender.Messages())
	}
	fetcher.err = maxsdk.ErrPhotoUnavailable
	if got := deliver(driver, "issue-photo-download-failed"); !strings.Contains(got, "Не удалось") {
		t.Fatalf("download failure = %q", got)
	}
	fetcher.err = nil
	if err := processor.Handle(context.Background(), photoItem(driver, "issue-photo-no-lease", now)); err == nil || !strings.Contains(err.Error(), "durable inbox") {
		t.Fatalf("photo without lease = %v", err)
	}
	for number := 1; number <= 3; number++ {
		fetcher.image = samplePhoto(t, uint8(40+number))
		key := fmt.Sprintf("issue-photo-%d", number)
		if got := deliver(driver, key); !strings.Contains(got, fmt.Sprintf("%d/3", number)) {
			t.Fatalf("saved photo %d = %q", number, got)
		}
		if number == 1 {
			if got := deliver(driver, "issue-photo-duplicate-hash"); !strings.Contains(got, "уже есть") {
				t.Fatalf("duplicate hash = %q", got)
			}
		}
	}
	if got := deliver(driver, "issue-photo-fourth"); !strings.Contains(got, "Четвёртое не добавлено") {
		t.Fatalf("fourth photo = %q", got)
	}
	if err := processor.Handle(context.Background(), photoItem(driver, "issue-photo-3", now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("replayed third photo: %v %+v", err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Conversation == nil || len(state.Conversation.Context.AssetIDs) != 3 || state.ConversationVersion != 5 || len(state.Checkout.Inspection.OccupiedSlots) != 8 || state.Checkout.Inspection.Version != checkout.Inspection.Version+1 {
		t.Fatalf("staged photos changed inspection: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "old-issue-help", help, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменился") {
		t.Fatalf("stale help: %v %+v", err, sender.Messages())
	}
}
