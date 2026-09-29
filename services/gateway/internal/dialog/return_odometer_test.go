package dialog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

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
