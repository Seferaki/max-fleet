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

func readyIssueCheckout(t *testing.T, actor *dataapi.Client, driver string) dataapi.Checkout {
	t.Helper()
	ctx := context.Background()
	checkout := setupPhotoCheckout(t, actor, driver)
	fuel, odometer := 50, int64(12001)
	result, err := actor.InspectionUpdate(ctx, driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{FuelLevel: &fuel, OdometerKM: &odometer}, "issue-setup-fields", nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil {
		t.Fatal(err)
	}
	for slot := 1; slot <= 8; slot++ {
		key := fmt.Sprintf("issue-setup-photo-%d", slot)
		uploaded, err := actor.UploadInspectionPhoto(ctx, driver, dataapi.InspectionPhotoInput{InspectionID: inspection.ID, Slot: slot, Version: inspection.Version, SourceEventKey: key, IdempotencyKey: key, ContentType: "image/png", Image: samplePhoto(t, uint8(slot))})
		if err != nil {
			t.Fatalf("photo %d: %v", slot, err)
		}
		inspection = uploaded.Inspection
	}
	if _, err := actor.InspectionConfirmPhotos(ctx, driver, inspection.ID, inspection.Version, "issue-setup-confirm", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Checkout == nil || !inspectionReadyForIssueQuestion(state.Checkout.Inspection) {
		t.Fatalf("issue setup: %+v %v", state, err)
	}
	return *state.Checkout
}

func TestNoNewIssuesPersistsAfterInspectionAndRejectsStaleChoice(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "issue-menu", now)); err != nil {
		t.Fatal(err)
	}
	var question string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "new-issues:") {
			question = row[0].Payload
		}
	}
	if question != fmt.Sprintf("new-issues:%s:%d", checkout.Inspection.ID, checkout.Inspection.Version) {
		t.Fatalf("question button = %q", question)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "issue-question", question, now)); err != nil {
		t.Fatal(err)
	}
	choices := sender.Messages()[1]
	if len(choices.Buttons) != 2 || choices.Buttons[0][0].Text != "Новых замечаний нет" || choices.Buttons[1][0].Text != "Есть замечание" {
		t.Fatalf("issue choices = %+v", choices.Buttons)
	}
	answer := choices.Buttons[0][0].Payload
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "other-issue", answer, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменился") {
		t.Fatalf("foreign answer: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "issue-no-lease", answer, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("answer without lease = %v", err)
	}
	event := callbackItem(driver, "no-new-issues", answer, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "issue-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[3].Text, "сохранено") {
		t.Fatalf("no-issue command: %+v %v %+v", result, err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Inspection.NewDamage == nil || *state.Checkout.Inspection.NewDamage || state.Checkout.NoNewIssues == nil || !*state.Checkout.NoNewIssues || state.Checkout.Inspection.Version != checkout.Inspection.Version+1 || state.Checkout.Version != checkout.Version+2 {
		t.Fatalf("no-issue state: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "issue-stale-question", question, now)); err != nil || !strings.Contains(sender.Messages()[4].Text, "изменился") {
		t.Fatalf("stale question: %v %+v", err, sender.Messages())
	}
}

func TestNewIssueAnswerCannotStartTripBeforeIssueSaved(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	payload := fmt.Sprintf("new-issues-set:%s:%d:yes", checkout.Inspection.ID, checkout.Inspection.Version)
	event := callbackItem(driver, "issue-yes", payload, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "issue-yes-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[0].Text, "Поездка не начнётся") {
		t.Fatalf("new issue answer: %+v %v %+v", result, err, sender.Messages())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Inspection.NewDamage == nil || !*state.Checkout.Inspection.NewDamage || state.Checkout.NoNewIssues != nil {
		t.Fatalf("new issue state: %+v %v", state, err)
	}
	if _, err := actor.CheckoutStart(context.Background(), driver, state.Checkout.ID, state.Checkout.Version, "unsafe-start", nil); err == nil {
		t.Fatal("trip started despite unsaved issue")
	}
}
