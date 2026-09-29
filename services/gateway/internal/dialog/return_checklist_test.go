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

func TestReturnChecklistAcceptsProblemsWithoutFalseAttestation(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyReturnDraft(t, actor, driver)
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil {
		t.Fatalf("return trip: %+v %v", state, err)
	}
	created, err := actor.ChallengeCreateReturn(context.Background(), driver, draft.ID, draft.Version, state.Trip.ID, state.Trip.Version-1, "checklist-setup-challenge", nil)
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
			if _, err := actor.ChallengeAnswer(context.Background(), driver, challenge.ID, challenge.Version, index, "checklist-setup-answer", nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	worker := inboxworker.Worker{ID: "checklist-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if err := processor.Handle(context.Background(), menuItem(driver, "checklist-menu", now)); err != nil {
		t.Fatal(err)
	}
	var next string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-check:") {
			next = row[0].Payload
		}
	}
	if next == "" {
		t.Fatalf("checklist menu missing: %+v", sender.Messages()[0])
	}
	for index, tc := range []struct {
		field      string
		choice     string
		button     int
		wantNotice string
	}{
		{"damage", "yes", 0, "Замечание нужно описать"},
		{"clean", "no", 1, "Замечание нужно описать"},
		{"parking", "no", 1, "Самостоятельное завершение"},
		{"keys_lock", "both_no", 3, "Самостоятельное завершение"},
	} {
		if err := processor.Handle(context.Background(), callbackItem(driver, fmt.Sprintf("checklist-question-%d", index), next, now)); err != nil {
			t.Fatal(err)
		}
		question := sender.Messages()[len(sender.Messages())-1]
		wantButtons := 2
		if tc.field == "keys_lock" {
			wantButtons = 4
		}
		if len(question.Buttons) != wantButtons || !strings.Contains(question.Text, "Машина закрыта, ключи возвращены") && tc.field == "keys_lock" || !strings.Contains(question.Buttons[tc.button][0].Payload, ":"+tc.field+":"+tc.choice) {
			t.Fatalf("question %s: %+v", tc.field, question)
		}
		answer := question.Buttons[tc.button][0].Payload
		if index == 0 {
			if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "checklist-foreign", answer, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменилась") {
				t.Fatalf("foreign checklist: %v %+v", err, sender.Messages())
			}
			if err := processor.Handle(context.Background(), callbackItem(driver, "checklist-no-lease", answer, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
				t.Fatalf("checklist without lease = %v", err)
			}
		}
		event := callbackItem(driver, fmt.Sprintf("checklist-save-%d", index), answer, now).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
			t.Fatalf("answer %s: %+v %v", tc.field, result, err)
		}
		response := sender.Messages()[len(sender.Messages())-1]
		if !strings.Contains(response.Text, tc.wantNotice) {
			t.Fatalf("problem %s was hidden: %+v", tc.field, response)
		}
		if index < 3 {
			next = response.Buttons[0][0].Payload
			if !strings.HasPrefix(next, "return-check:") {
				t.Fatalf("next question %s: %+v", tc.field, response)
			}
		}
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Trip == nil || state.Trip.Status != "returning" || state.Return.Inspection.NewDamage == nil || !*state.Return.Inspection.NewDamage || state.Return.Inspection.CabinClean == nil || *state.Return.Inspection.CabinClean || state.Return.Inspection.ParkingAllowed == nil || *state.Return.Inspection.ParkingAllowed || state.Return.Inspection.KeysReturned == nil || *state.Return.Inspection.KeysReturned || state.Return.Inspection.CarLocked == nil || *state.Return.Inspection.CarLocked || nextReturnCheckField(state.Return.Inspection) != "" {
		t.Fatalf("problem answers changed or missing: %+v %v", state, err)
	}
}

func TestReturnChecklistCombinedChoiceMapsAllTruthCombinations(t *testing.T) {
	for _, tc := range []struct {
		choice string
		keys   bool
		locked bool
	}{
		{"both_yes", true, true},
		{"keys_no", false, true},
		{"lock_no", true, false},
		{"both_no", false, false},
	} {
		input, ok := returnCheckInput("keys_lock", tc.choice)
		if !ok || input.KeysReturned == nil || input.CarLocked == nil || *input.KeysReturned != tc.keys || *input.CarLocked != tc.locked {
			t.Fatalf("choice %s mapped to %+v", tc.choice, input)
		}
		inspection := dataapi.Inspection{KeysReturned: input.KeysReturned, CarLocked: input.CarLocked}
		if !returnCheckMatches(inspection, "keys_lock", tc.choice) {
			t.Fatalf("choice %s did not match saved fields: %+v", tc.choice, inspection)
		}
	}
	if _, ok := returnCheckInput("keys_lock", "yes"); ok {
		t.Fatal("ambiguous combined yes answer was accepted")
	}
}

func TestReturnChecklistReplyRecoveryKeepsSavedAnswer(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyReturnDraft(t, actor, driver)
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil {
		t.Fatalf("return trip: %+v %v", state, err)
	}
	created, err := actor.ChallengeCreateReturn(context.Background(), driver, draft.ID, draft.Version, state.Trip.ID, state.Trip.Version-1, "checklist-retry-challenge", nil)
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
			if _, err := actor.ChallengeAnswer(context.Background(), driver, challenge.ID, challenge.Version, index, "checklist-retry-answer", nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil {
		t.Fatalf("checklist state: %+v %v", state, err)
	}
	noDamage, clean, parkingAllowed := false, true, true
	if _, err := actor.InspectionUpdate(context.Background(), driver, state.Return.Inspection.ID, state.Return.Inspection.Version, dataapi.InspectionUpdateInput{
		NewDamage:      &noDamage,
		CabinClean:     &clean,
		ParkingAllowed: &parkingAllowed,
	}, "checklist-retry-setup-prior-answers", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || nextReturnCheckField(state.Return.Inspection) != "keys_lock" {
		t.Fatalf("composite question was not next: %+v %v", state, err)
	}
	payload := fmt.Sprintf("return-check-set:%s:%d:keys_lock:keys_no", state.Return.Inspection.ID, state.Return.Inspection.Version)
	event := callbackItem(driver, "checklist-retry-keys-lock", payload, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender := &failOnePhotoReply{fail: true}
	worker := inboxworker.Worker{ID: "checklist-retry-worker", Store: store, Processor: Bootstrap{Data: actor, Commands: actor, MAX: sender}, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost checklist reply: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.KeysReturned == nil || *state.Return.Inspection.KeysReturned || state.Return.Inspection.CarLocked == nil || !*state.Return.Inspection.CarLocked {
		t.Fatalf("saved combined answer lost: %+v %v", state, err)
	}
	version := state.Return.Inspection.Version
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[0].Text, "уже сохранён") {
		t.Fatalf("checklist reply recovery: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.Version != version {
		t.Fatalf("replay changed inspection: %+v %v", state, err)
	}
}
