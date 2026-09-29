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
	if err := processor.Handle(ctx, callbackItem(driver, "after-issue-categories", button, now)); err != nil || len(sender.Messages()[3].Buttons) != 7 {
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

func TestReturnIssuePhotosUseInspectionScopeWithoutAfterSlots(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	draftReturn := readyChecklistDraft(t, actor, driver)
	state, err := actor.State(ctx, driver)
	if err != nil || state.Trip == nil {
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
	category, description, kind := "cleanliness", "Грязный салон", "photo"
	input := dataapi.ConversationSaveInput{Flow: "issue_after", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &draftReturn.Inspection.ID, TripID: &state.Trip.ID, ReturnID: &draftReturn.ID, VehicleID: &state.Trip.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description}}
	if _, err := actor.ConversationSave(ctx, driver, me.Employee.ID, 1, input, "after-photo-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	fetcher := &syntheticPhotoFetcher{image: samplePhoto(t, 90)}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, PhotoStore: actor, Photos: fetcher, MAX: sender}
	if err := processor.Handle(ctx, menuItem(driver, "after-photo-menu", now)); err != nil {
		t.Fatal(err)
	}
	var help string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-issue-photos:") {
			help = row[0].Payload
		}
	}
	if help == "" {
		t.Fatal("after issue photo entry absent")
	}
	if err := processor.Handle(ctx, callbackItem(driver, "after-photo-help", help, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "0/3") {
		t.Fatalf("photo help: %v %+v", err, sender.Messages())
	}
	worker := inboxworker.Worker{ID: "after-photo-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	deliver := func(key string) string {
		t.Helper()
		event := photoItem(driver, key, now).Event
		if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
			t.Fatalf("photo %s: %+v %v", key, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1].Text
	}
	fetcher.err = maxsdk.ErrPhotoUnavailable
	if got := deliver("after-photo-download-failed"); !strings.Contains(got, "Не удалось") {
		t.Fatalf("download error: %q", got)
	}
	fetcher.err = nil
	for number := 1; number <= 3; number++ {
		fetcher.image = samplePhoto(t, uint8(90+number))
		if got := deliver(fmt.Sprintf("after-photo-%d", number)); !strings.Contains(got, fmt.Sprintf("%d/3", number)) {
			t.Fatalf("photo %d: %q", number, got)
		}
	}
	if got := deliver("after-photo-fourth"); !strings.Contains(got, "Четвёртое не добавлено") {
		t.Fatalf("fourth photo: %q", got)
	}
	if err := processor.Handle(ctx, photoItem(driver, "after-photo-3", now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("replayed photo: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || len(state.Conversation.Context.AssetIDs) != 3 || state.ConversationVersion != 5 || state.Return == nil || len(state.Return.Inspection.OccupiedSlots) != 0 {
		t.Fatalf("issue photos entered after slots: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, callbackItem(driver, "after-photo-old-help", help, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменился") {
		t.Fatalf("stale help: %v %+v", err, sender.Messages())
	}
}
