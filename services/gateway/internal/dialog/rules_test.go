package dialog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestRulesRequireCurrentVersionExplicitClickAndLease(t *testing.T) {
	current := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	available := true
	page, err := actor.Vehicles(context.Background(), driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	vehicle := page.Items[0]
	created, err := actor.CheckoutCreate(context.Background(), driver, vehicle.ID, vehicle.Version, "rules-setup-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	challengeResult, err := actor.ChallengeCreateTake(context.Background(), driver, checkout.ID, checkout.Version, vehicle.ID, vehicle.Version, "rules-setup-challenge", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](challengeResult)
	if err != nil {
		t.Fatal(err)
	}
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	correct := -1
	for index, option := range challenge.Options {
		if option == a+b {
			correct = index
		}
	}
	if correct < 0 {
		t.Fatal("correct math answer missing")
	}
	if _, err := actor.ChallengeAnswer(context.Background(), driver, challenge.ID, challenge.Version, correct, "rules-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Step != "rules" {
		t.Fatalf("rules setup: %+v %v", state, err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Location: time.UTC}
	worker := inboxworker.Worker{ID: "rules-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if err := processor.Handle(context.Background(), menuItem(driver, "rules-menu", current)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[0]
	if len(menu.Buttons) != 2 || !strings.HasPrefix(menu.Buttons[0][0].Payload, "rules:") {
		t.Fatalf("rules recovery menu: %+v", menu)
	}
	deliver := func(maxID, key, payload string) maxsdk.RecordedText {
		t.Helper()
		event := callbackItem(maxID, key, payload, current).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		result, err := worker.RunOnce(context.Background(), 1)
		if err != nil || result.Acked != 1 {
			t.Fatalf("rules event %s: %+v %v", key, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1]
	}
	other := deliver("8000000000000000002", "rules-other", menu.Buttons[0][0].Payload)
	if !strings.Contains(other.Text, "изменился") {
		t.Fatalf("other actor rules: %+v", other)
	}
	shown := deliver(driver, "rules-show", menu.Buttons[0][0].Payload)
	rules, err := actor.CurrentRules(context.Background(), driver)
	if err != nil || !strings.Contains(shown.Text, rules.Body) || !strings.Contains(shown.Text, rules.VersionLabel) || len(shown.Buttons) != 2 {
		t.Fatalf("rules not shown: %+v %v", shown, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.RulesAcceptedAt != nil || state.Checkout.Step != "rules" {
		t.Fatalf("showing rules accepted them: %+v %v", state, err)
	}
	acceptPayload := shown.Buttons[0][0].Payload
	if err := processor.Handle(context.Background(), callbackItem(driver, "rules-no-lease", acceptPayload, current)); err == nil {
		t.Fatal("rules accepted without inbox lease")
	}
	parts := strings.Split(acceptPayload, ":")
	if len(parts) != 4 {
		t.Fatalf("bad acceptance payload: %q", acceptPayload)
	}
	forged := strings.Join(parts[:3], ":") + ":99999999-9999-4999-8999-999999999999"
	refreshed := deliver(driver, "rules-old-version", forged)
	if !strings.Contains(refreshed.Text, "Правила изменились") || len(refreshed.Buttons) != 2 {
		t.Fatalf("stale rules version: %+v", refreshed)
	}
	confirmed := deliver(driver, "rules-accept", acceptPayload)
	if !strings.Contains(confirmed.Text, "приняты") {
		t.Fatalf("rules acceptance: %+v", confirmed)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Step != "inspection" || state.Checkout.RulesAcceptedAt == nil || state.Checkout.RulesVersionID == nil || *state.Checkout.RulesVersionID != rules.ID || state.Trip != nil {
		t.Fatalf("rules state: %+v %v", state, err)
	}
	acceptedVersion := state.Checkout.Version
	duplicateEvent := callbackItem(driver, "rules-accept", acceptPayload, current).Event
	storedAgain, err := store.StoreInbox(context.Background(), duplicateEvent, maxsdk.InboxIdempotencyKey(duplicateEvent))
	if err != nil || !storedAgain.Duplicate {
		t.Fatalf("duplicate acceptance event: %+v %v", storedAgain, err)
	}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 0 {
		t.Fatalf("duplicate acceptance processed twice: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Version != acceptedVersion {
		t.Fatalf("duplicate acceptance changed state: %+v %v", state, err)
	}
	stale := deliver(driver, "rules-stale-callback", acceptPayload)
	if !strings.Contains(stale.Text, "изменился") {
		t.Fatalf("stale callback was not rejected: %+v", stale)
	}
	current = current.Add(16 * time.Minute)
	expired := deliver(driver, "rules-expired", acceptPayload)
	if !strings.Contains(expired.Text, "истёк") {
		t.Fatalf("expired hold callback: %+v", expired)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout != nil || state.Trip != nil {
		t.Fatalf("expired hold restored: %+v %v", state, err)
	}
}

func TestLongRulesAreSentBeforeAcceptanceButton(t *testing.T) {
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{MAX: sender}
	rules := dataapi.Rules{ID: "90000000-0000-4000-8000-000000000001", VersionLabel: "demo-v1", Body: strings.Repeat("Правило. ", 1100)}
	checkout := dataapi.Checkout{ID: "20000000-0000-4000-8000-000000000001", Version: 2}
	if err := processor.sendRules(context.Background(), 8000000000000000001, checkout, rules, ""); err != nil {
		t.Fatal(err)
	}
	messages := sender.Messages()
	if len(messages) < 3 {
		t.Fatalf("long rules not split: %d", len(messages))
	}
	for index, message := range messages {
		if utf8.RuneCountInString(message.Text) > 4000 || index < len(messages)-1 && len(message.Buttons) != 0 {
			t.Fatalf("unsafe rules part %d: %d runes, %d buttons", index, utf8.RuneCountInString(message.Text), len(message.Buttons))
		}
	}
	if len(messages[len(messages)-1].Buttons) != 2 {
		t.Fatal("acceptance button absent from final part")
	}
}
