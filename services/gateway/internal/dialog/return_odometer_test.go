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

func TestReturnOdometerLargeIncreaseRequiresConfirmationAndRecovers(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyChecklistDraft(t, actor, driver)
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Trip.BeforeInspection.OdometerKM == nil {
		t.Fatalf("return baseline: %+v %v", state, err)
	}
	baseline := *state.Trip.BeforeInspection.OdometerKM
	largeValue := baseline + returnOdometerConfirmationThresholdKM + 1
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	worker := inboxworker.Worker{ID: "return-odo-confirm-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}

	command := fmt.Sprintf("/odometer %d", largeValue)
	event := menuItem(driver, "return-odo-confirm-input", now)
	event.Event.Payload.Text = &command
	if _, err := store.StoreInbox(context.Background(), event.Event, maxsdk.InboxIdempotencyKey(event.Event)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
		t.Fatalf("large odometer prompt: %+v %v", result, err)
	}
	message := sender.Messages()[0]
	if !strings.Contains(message.Text, fmt.Sprintf("прирост составит %d км", returnOdometerConfirmationThresholdKM+1)) || len(message.Buttons) != 2 {
		t.Fatalf("large odometer confirmation prompt: %+v", message)
	}
	confirmPayload := message.Buttons[0][0].Payload
	wantConfirm := fmt.Sprintf("return-odometer-confirm:%s:%d:%d", draft.Inspection.ID, draft.Inspection.Version, largeValue)
	if confirmPayload != wantConfirm || message.Buttons[1][0].Payload != fmt.Sprintf("return-odometer:%s:%d", draft.Inspection.ID, draft.Inspection.Version) {
		t.Fatalf("confirmation choices: %+v", message.Buttons)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.OdometerKM != nil || state.Return.Inspection.Version != draft.Inspection.Version {
		t.Fatalf("large value saved before confirmation: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-odo-reenter", message.Buttons[1][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "/odometer N") {
		t.Fatalf("reenter callback: %v %+v", err, sender.Messages())
	}
	stalePayload := fmt.Sprintf("return-odometer-confirm:%s:%d:%d", draft.Inspection.ID, draft.Inspection.Version+1, largeValue)
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-odo-stale-confirm", stalePayload, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменился") {
		t.Fatalf("stale confirmation: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.OdometerKM != nil {
		t.Fatalf("stale callback changed odometer: %+v %v", state, err)
	}

	confirmEvent := callbackItem(driver, "return-odo-confirm-save", confirmPayload, now).Event
	if _, err := store.StoreInbox(context.Background(), confirmEvent, maxsdk.InboxIdempotencyKey(confirmEvent)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost confirmation reply: %+v %v", result, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранён") {
		t.Fatalf("confirmation retry: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.OdometerKM == nil || *state.Return.Inspection.OdometerKM != largeValue || state.Return.Inspection.Version != draft.Inspection.Version+1 {
		gotValue := int64(-1)
		gotVersion := int64(-1)
		if state.Return != nil {
			gotVersion = state.Return.Inspection.Version
			if state.Return.Inspection.OdometerKM != nil {
				gotValue = *state.Return.Inspection.OdometerKM
			}
		}
		t.Fatalf("confirmed odometer: value=%d version=%d want value=%d version=%d err=%v replies=%+v", gotValue, gotVersion, largeValue, draft.Inspection.Version+1, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.Version != draft.Inspection.Version+1 {
		t.Fatalf("confirmation retry changed version: %+v %v", state, err)
	}
}

func TestReturnOdometerThresholdIsStrictlyGreaterThan1000KM(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyChecklistDraft(t, actor, driver)
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Trip.BeforeInspection.OdometerKM == nil {
		t.Fatalf("return baseline: %+v %v", state, err)
	}
	boundaryValue := *state.Trip.BeforeInspection.OdometerKM + returnOdometerConfirmationThresholdKM
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	worker := inboxworker.Worker{ID: "return-odo-boundary-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	command := fmt.Sprintf("/odometer %d", boundaryValue)
	event := menuItem(driver, "return-odo-boundary-input", now)
	event.Event.Payload.Text = &command
	if _, err := store.StoreInbox(context.Background(), event.Event, maxsdk.InboxIdempotencyKey(event.Event)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
		t.Fatalf("boundary odometer save: %+v %v", result, err)
	}
	if messages := sender.Messages(); len(messages) != 1 || len(messages[0].Buttons) != 0 || !strings.Contains(messages[0].Text, "сохранён") {
		t.Fatalf("1000 km should save without extra confirmation: %+v", messages)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.OdometerKM == nil || *state.Return.Inspection.OdometerKM != boundaryValue || state.Return.Inspection.Version != draft.Inspection.Version+1 {
		t.Fatalf("1000 km odometer: %+v %v", state, err)
	}
}

func TestReturnOdometerBaselineFormatAndLostReply(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyChecklistDraft(t, actor, driver)
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "return-odo-menu", now)); err != nil {
		t.Fatal(err)
	}
	var prompt string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-odometer:") {
			prompt = row[0].Payload
		}
	}
	if prompt == "" {
		t.Fatalf("return odometer button missing: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-odo-prompt", prompt, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "12001 км") {
		t.Fatalf("odometer prompt: %v %+v", err, sender.Messages())
	}
	command := func(key, value string) dataapi.InboxClaimItem {
		item := menuItem(driver, key, now)
		item.Event.Payload.Text = &value
		return item
	}
	if err := processor.Handle(context.Background(), command("return-odo-negative", "/odometer -1")); err != nil || !strings.Contains(sender.Messages()[2].Text, "неотрицательное") {
		t.Fatalf("negative odometer: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), command("return-odo-low", "/odometer 12000")); err != nil || !strings.Contains(sender.Messages()[3].Text, "меньше показания") {
		t.Fatalf("rollback odometer: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), command("return-odo-no-lease", "/odometer 12050")); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("odometer without lease = %v", err)
	}
	item := command("return-odo-save", "/odometer 12050")
	if _, err := store.StoreInbox(context.Background(), item.Event, maxsdk.InboxIdempotencyKey(item.Event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "return-odo-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost odometer reply: %+v %v", result, err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.OdometerKM == nil || *state.Return.Inspection.OdometerKM != 12050 || state.Return.Inspection.Version != draft.Inspection.Version+1 {
		t.Fatalf("saved odometer: %+v %v", state, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранён") {
		t.Fatalf("odometer reply recovery: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.Version != draft.Inspection.Version+1 {
		t.Fatalf("odometer replay changed version: %+v %v", state, err)
	}
}
