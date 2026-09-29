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

func readyChecklistDraft(t *testing.T, actor *dataapi.Client, driver string) dataapi.Return {
	t.Helper()
	draft := readyReturnDraft(t, actor, driver)
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil {
		t.Fatalf("return trip: %+v %v", state, err)
	}
	created, err := actor.ChallengeCreateReturn(context.Background(), driver, draft.ID, draft.Version, state.Trip.ID, state.Trip.Version-1, "ready-checklist-challenge", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](created)
	if err != nil {
		t.Fatal(err)
	}
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	for index, value := range challenge.Options {
		if value == a+b {
			if _, err := actor.ChallengeAnswer(context.Background(), driver, challenge.ID, challenge.Version, index, "ready-checklist-answer", nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Step != "checklist" {
		t.Fatalf("checklist draft: %+v %v", state, err)
	}
	return *state.Return
}

func TestReturnFuelVersionRightsAndReplyRecovery(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyChecklistDraft(t, actor, driver)
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "return-fuel-menu", now)); err != nil {
		t.Fatal(err)
	}
	var fuel string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-fuel:") {
			fuel = row[0].Payload
		}
	}
	if fuel != fmt.Sprintf("return-fuel:%s:%d", draft.Inspection.ID, draft.Inspection.Version) {
		t.Fatalf("return fuel button: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-fuel-choose", fuel, now)); err != nil || len(sender.Messages()[1].Buttons) != 5 {
		t.Fatalf("fuel choices: %v %+v", err, sender.Messages())
	}
	selected := sender.Messages()[1].Buttons[3][0].Payload
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "return-fuel-foreign", selected, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменился") {
		t.Fatalf("foreign fuel: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-fuel-invalid", strings.TrimSuffix(selected, ":75")+":42", now)); err != nil || !strings.Contains(sender.Messages()[3].Text, "Некорректный") {
		t.Fatalf("invalid fuel: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-fuel-no-lease", selected, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("fuel without lease = %v", err)
	}
	event := callbackItem(driver, "return-fuel-save", selected, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "return-fuel-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost fuel reply: %+v %v", result, err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.FuelLevel == nil || *state.Return.Inspection.FuelLevel != 75 || state.Return.Inspection.Version != draft.Inspection.Version+1 {
		t.Fatalf("saved fuel: %+v %v", state, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("fuel reply recovery: %+v %v %+v", result, err, sender.Messages())
	}
}
