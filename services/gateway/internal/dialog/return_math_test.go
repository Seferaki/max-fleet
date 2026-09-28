package dialog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func readyReturnDraft(t *testing.T, actor *dataapi.Client, driver string) dataapi.Return {
	t.Helper()
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "math-return-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(context.Background(), driver, checkout.ID, state.Checkout.Version, "math-return-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutStart(context.Background(), driver, checkout.ID, state.Checkout.Version, "math-return-setup-start", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil {
		t.Fatalf("active trip: %+v %v", state, err)
	}
	result, err := actor.TripBeginReturn(context.Background(), driver, state.Trip.ID, state.Trip.Version, "math-return-setup-begin", nil)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := dataapi.DecodeAggregate[dataapi.Return](result)
	if err != nil || draft.Step != "math" {
		t.Fatalf("return draft: %+v %v", draft, err)
	}
	return draft
}

func TestReturnMathWrongRightAndLostReply(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyReturnDraft(t, actor, driver)
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	worker := inboxworker.Worker{ID: "return-math-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	deliver := func(key, payload string) {
		t.Helper()
		event := callbackItem(driver, key, payload, current).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
			t.Fatalf("return math %s: %+v %v", key, result, err)
		}
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "return-math-menu", now)); err != nil {
		t.Fatal(err)
	}
	var math string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-math:") {
			math = row[0].Payload
		}
	}
	if math != fmt.Sprintf("return-math:%s:%d", draft.ID, draft.Version) {
		t.Fatalf("math menu: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "math-no-lease", math, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("return math without lease = %v", err)
	}
	deliver("return-math-create", math)
	question := sender.Messages()[1]
	var a, b int
	for _, line := range strings.Split(question.Text, "\n") {
		if _, err := fmt.Sscanf(line, "%d + %d = ?", &a, &b); err == nil {
			break
		}
	}
	var wrong string
	for _, row := range question.Buttons {
		value, err := strconv.Atoi(row[0].Text)
		if err == nil && value != a+b {
			wrong = row[0].Payload
			break
		}
	}
	if wrong == "" {
		t.Fatalf("wrong answer missing: %+v", question)
	}
	deliver("return-math-wrong", wrong)
	if !strings.Contains(sender.Messages()[2].Text, "Осталось попыток: 2") {
		t.Fatalf("wrong answer: %+v", sender.Messages()[2])
	}
	var right string
	for _, row := range sender.Messages()[2].Buttons {
		value, err := strconv.Atoi(row[0].Text)
		if err == nil && value == a+b {
			right = row[0].Payload
		}
	}
	if right == "" {
		t.Fatal("correct answer missing after wrong attempt")
	}
	event := callbackItem(driver, "return-math-right", right, current).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost math reply: %+v %v", result, err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.ID != draft.ID || state.Return.Step != "checklist" || state.Return.IntentConfirmedAt == nil || state.Trip == nil || state.Trip.Status != "returning" {
		t.Fatalf("math committed: %+v %v", state, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже пройдена") {
		t.Fatalf("math reply recovery: %+v %v %+v", result, err, sender.Messages())
	}
}
