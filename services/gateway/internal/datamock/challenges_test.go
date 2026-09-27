package datamock

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func challengeChoices(t *testing.T, challenge dataapi.Challenge) (int, int) {
	t.Helper()
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	correct, wrong := -1, -1
	for i, option := range challenge.Options {
		if option == a+b {
			correct = i
		} else {
			wrong = i
		}
	}
	if correct < 0 || wrong < 0 || len(challenge.Options) != 4 {
		t.Fatalf("invalid challenge: %+v", challenge)
	}
	return correct, wrong
}

func TestTakeChallengePersistsAndRulesRequireAnswer(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	holdResult, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "math-hold-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](holdResult)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CheckoutAcceptRules(ctx, driverID, hold.ID, hold.Version, mock.rules.ID, "premature-rules-1", nil)
	expectAPIError(t, err, "INVALID_STATE")
	created, err := client.ChallengeCreateTake(ctx, driverID, hold.ID, hold.Version, firstVehicleID, 1, "math-create-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](created)
	if err != nil || challenge.Purpose != "take" || challenge.ExpiresAt.Sub(now) != 5*time.Minute {
		t.Fatalf("challenge: %+v %v", challenge, err)
	}
	correct, wrong := challengeChoices(t, challenge)
	_, err = client.ChallengeAnswer(ctx, "8000000000000000002", challenge.ID, challenge.Version, correct, "foreign-answer-1", nil)
	expectAPIError(t, err, "NOT_FOUND")
	wrongResult, err := client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version, wrong, "math-wrong-1", nil)
	if err != nil || wrongResult.Correct == nil || *wrongResult.Correct || wrongResult.AttemptsRemaining == nil || *wrongResult.AttemptsRemaining != 2 {
		t.Fatalf("wrong answer: %+v %v", wrongResult, err)
	}
	repeated, err := client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version, wrong, "math-wrong-1", nil)
	if err != nil || repeated.AttemptsRemaining == nil || *repeated.AttemptsRemaining != 2 {
		t.Fatalf("idempotent answer: %+v %v", repeated, err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client = commandClient(t, restarted)
	repeated, err = client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version, wrong, "math-wrong-1", nil)
	if err != nil || repeated.AttemptsRemaining == nil || *repeated.AttemptsRemaining != 2 {
		t.Fatalf("idempotent answer after restart: %+v %v", repeated, err)
	}
	_, err = client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version, correct, "math-wrong-1", nil)
	expectAPIError(t, err, "IDEMPOTENCY_CONFLICT")
	answered, err := client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version+1, correct, "math-correct-1", nil)
	if err != nil || answered.Correct == nil || !*answered.Correct {
		t.Fatalf("correct after restart: %+v %v", answered, err)
	}
	current, err := client.Checkout(ctx, driverID, hold.ID)
	if err != nil || current.IntentConfirmedAt == nil || current.Step != "rules" {
		t.Fatalf("intent state: %+v %v", current, err)
	}
	_, err = client.CheckoutAcceptRules(ctx, driverID, hold.ID, current.Version, "90000000-0000-4000-8000-000000000099", "wrong-rules-1", nil)
	expectAPIError(t, err, "STALE_VERSION")
	accepted, err := client.CheckoutAcceptRules(ctx, driverID, hold.ID, current.Version, restarted.rules.ID, "accept-rules-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := dataapi.DecodeAggregate[dataapi.Checkout](accepted)
	if err != nil || ready.RulesAcceptedAt == nil || ready.RulesVersionID == nil || *ready.RulesVersionID != restarted.rules.ID || ready.Step != "inspection" {
		t.Fatalf("accepted rules: %+v %v", ready, err)
	}
	badOdometer := int64(11999)
	_, err = client.InspectionUpdate(ctx, driverID, ready.Inspection.ID, ready.Inspection.Version, dataapi.InspectionUpdateInput{OdometerKM: &badOdometer}, "odo-backward-1", nil)
	expectAPIError(t, err, "ODOMETER_ROLLBACK")
	fuel, odometer := 75, int64(12010)
	updatedResult, err := client.InspectionUpdate(ctx, driverID, ready.Inspection.ID, ready.Inspection.Version, dataapi.InspectionUpdateInput{FuelLevel: &fuel, OdometerKM: &odometer}, "inspection-data-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := dataapi.DecodeAggregate[dataapi.Inspection](updatedResult)
	if err != nil || updated.FuelLevel == nil || *updated.FuelLevel != 75 || updated.OdometerKM == nil || *updated.OdometerKM != 12010 {
		t.Fatalf("inspection data: %+v %v", updated, err)
	}
	_, err = client.CheckoutSetNoNewIssues(ctx, "8000000000000000002", hold.ID, ready.Version+1, "foreign-issues-1", nil)
	expectAPIError(t, err, "NOT_FOUND")
	issueResult, err := client.CheckoutSetNoNewIssues(ctx, driverID, hold.ID, ready.Version+1, "no-issues-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	withIssues, err := dataapi.DecodeAggregate[dataapi.Checkout](issueResult)
	if err != nil || withIssues.NoNewIssues == nil || !*withIssues.NoNewIssues {
		t.Fatalf("no new issues: %+v %v", withIssues, err)
	}
	invalidFuel := 33
	if _, err := client.InspectionUpdate(ctx, driverID, updated.ID, updated.Version, dataapi.InspectionUpdateInput{FuelLevel: &invalidFuel}, "invalid-fuel-1", nil); err == nil {
		t.Fatal("unsupported fuel level accepted")
	}
	_, err = client.CheckoutStart(ctx, driverID, hold.ID, withIssues.Version, "early-start-1", nil)
	expectAPIError(t, err, "PHOTO_SET_INCOMPLETE")
	photoVersion := updated.Version
	for slot := 1; slot <= 8; slot++ {
		photo, err := client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: updated.ID, Slot: slot, Version: photoVersion, SourceEventKey: fmt.Sprintf("start-event-%d", slot), IdempotencyKey: fmt.Sprintf("start-photo-%d", slot), ContentType: "image/png", Image: syntheticPNG(t, uint8(slot))})
		if err != nil {
			t.Fatalf("start slot %d: %v", slot, err)
		}
		photoVersion = photo.Inspection.Version
	}
	_, err = client.CheckoutStart(ctx, driverID, hold.ID, withIssues.Version+8, "unconfirmed-start-1", nil)
	expectAPIError(t, err, "INVALID_STATE")
	if _, err := client.InspectionConfirmPhotos(ctx, driverID, updated.ID, photoVersion, "start-photo-confirm-1", nil); err != nil {
		t.Fatal(err)
	}
	beforeStart, err := client.Checkout(ctx, driverID, hold.ID)
	if err != nil {
		t.Fatal(err)
	}
	save := restarted.saveSnapshot
	restarted.saveSnapshot = func(stateSnapshot) error { return errors.New("injected snapshot failure") }
	_, err = client.CheckoutStart(ctx, driverID, hold.ID, beforeStart.Version, "failed-start-1", nil)
	expectAPIError(t, err, "TEMPORARY_FAILURE")
	restarted.saveSnapshot = save
	stillHolding, err := client.Checkout(ctx, driverID, hold.ID)
	if err != nil || stillHolding.Status != "holding" || stillHolding.Version != beforeStart.Version || len(restarted.trips) != 0 {
		t.Fatalf("failed save changed state: %+v %v", stillHolding, err)
	}
	started, err := client.CheckoutStart(ctx, driverID, hold.ID, beforeStart.Version, "start-trip-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	trip, err := dataapi.DecodeAggregate[dataapi.Trip](started)
	if err != nil || trip.Status != "active" || trip.BeforeInspection.Status != "finalized" || trip.BeforeInspection.PhotosConfirmedAt == nil {
		t.Fatalf("trip: %+v %v", trip, err)
	}
	repeat, err := client.CheckoutStart(ctx, driverID, hold.ID, beforeStart.Version, "start-trip-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	repeatedTrip, _ := dataapi.DecodeAggregate[dataapi.Trip](repeat)
	if repeatedTrip.ID != trip.ID {
		t.Fatal("duplicate start created another trip")
	}
	_, err = client.Trip(ctx, "8000000000000000002", trip.ID)
	expectAPIError(t, err, "NOT_FOUND")
	last, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client = commandClient(t, last)
	state, err := client.State(ctx, driverID)
	if err != nil || state.Trip == nil || state.Trip.ID != trip.ID || state.Checkout != nil {
		t.Fatalf("restored active trip: %+v %v", state, err)
	}
	me, err := client.Me(ctx, driverID)
	if err != nil || me.Employee == nil || me.Employee.ActiveTripID == nil || *me.Employee.ActiveTripID != trip.ID {
		t.Fatalf("restored active employee: %+v %v", me, err)
	}
	_, err = client.CheckoutCreate(ctx, driverID, "10000000-0000-4000-8000-000000000002", 1, "another-hold-1", nil)
	expectAPIError(t, err, "USER_BUSY")
	_, err = client.TripBeginReturn(ctx, "8000000000000000002", trip.ID, trip.Version, "foreign-return-1", nil)
	expectAPIError(t, err, "NOT_FOUND")
	beginResult, err := client.TripBeginReturn(ctx, driverID, trip.ID, trip.Version, "begin-return-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := dataapi.DecodeAggregate[dataapi.Return](beginResult)
	if err != nil || draft.Status != "draft" || draft.Step != "math" || draft.Inspection.Phase != "after" || len(draft.Inspection.MissingSlots) != 8 {
		t.Fatalf("fresh return: %+v %v", draft, err)
	}
	again, err := client.TripBeginReturn(ctx, driverID, trip.ID, trip.Version, "begin-return-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	repeatedDraft, _ := dataapi.DecodeAggregate[dataapi.Return](again)
	if repeatedDraft.ID != draft.ID {
		t.Fatal("duplicate begin_return created another draft")
	}
	_, err = client.TripBeginReturn(ctx, driverID, trip.ID, trip.Version+1, "begin-second-1", nil)
	expectAPIError(t, err, "INVALID_STATE")
	state, err = client.State(ctx, driverID)
	if err != nil || state.Return == nil || state.Return.ID != draft.ID || state.Trip == nil || state.Trip.Status != "returning" {
		t.Fatalf("return state: %+v %v", state, err)
	}
	cancelledResult, err := client.ReturnCancel(ctx, driverID, draft.ID, draft.Version, "cancel-return-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := dataapi.DecodeAggregate[dataapi.Return](cancelledResult)
	if err != nil || cancelled.Status != "cancelled" || cancelled.Inspection.Status != "abandoned" {
		t.Fatalf("cancelled return: %+v %v", cancelled, err)
	}
	active, err := client.Trip(ctx, driverID, trip.ID)
	if err != nil || active.Status != "active" || active.ReturnID != nil {
		t.Fatalf("trip after cancel: %+v %v", active, err)
	}
	freshResult, err := client.TripBeginReturn(ctx, driverID, trip.ID, active.Version, "begin-again-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := dataapi.DecodeAggregate[dataapi.Return](freshResult)
	if fresh.ID == draft.ID || fresh.Inspection.ID == draft.Inspection.ID || len(fresh.Inspection.OccupiedSlots) != 0 || fresh.ParkingLocation != nil {
		t.Fatalf("new return inherited cancelled data: %+v", fresh)
	}
	_, err = client.Return(ctx, "8000000000000000002", fresh.ID)
	expectAPIError(t, err, "NOT_FOUND")
	last, err = NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client = commandClient(t, last)
	state, err = client.State(ctx, driverID)
	if err != nil || state.Return == nil || state.Return.ID != fresh.ID || state.Trip == nil || state.Trip.Status != "returning" {
		t.Fatalf("restored fresh return: %+v %v", state, err)
	}
	_, err = client.ChallengeCreateReturn(ctx, "8000000000000000002", fresh.ID, fresh.Version, trip.ID, active.Version, "foreign-math-return", nil)
	expectAPIError(t, err, "NOT_FOUND")
	returnChallengeResult, err := client.ChallengeCreateReturn(ctx, driverID, fresh.ID, fresh.Version, trip.ID, active.Version, "math-return-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	returnChallenge, err := dataapi.DecodeAggregate[dataapi.Challenge](returnChallengeResult)
	if err != nil || returnChallenge.Purpose != "return" {
		t.Fatalf("return challenge: %+v %v", returnChallenge, err)
	}
	right, _ := challengeChoices(t, returnChallenge)
	returnAnswer, err := client.ChallengeAnswer(ctx, driverID, returnChallenge.ID, returnChallenge.Version, right, "math-return-answer", nil)
	if err != nil || returnAnswer.Correct == nil || !*returnAnswer.Correct {
		t.Fatalf("return answer: %+v %v", returnAnswer, err)
	}
	confirmedReturn, err := client.Return(ctx, driverID, fresh.ID)
	if err != nil || confirmedReturn.IntentConfirmedAt == nil || confirmedReturn.Step != "checklist" {
		t.Fatalf("confirmed return intent: %+v %v", confirmedReturn, err)
	}
}

func TestTakeChallengeThreeErrorsAndTTL(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	holdResult, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "hold-three-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, _ := dataapi.DecodeAggregate[dataapi.Checkout](holdResult)
	created, err := client.ChallengeCreateTake(ctx, driverID, hold.ID, hold.Version, firstVehicleID, 1, "challenge-three-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, _ := dataapi.DecodeAggregate[dataapi.Challenge](created)
	_, wrong := challengeChoices(t, challenge)
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version, wrong, fmt.Sprintf("wrong-%d-key", attempt), nil)
		if err != nil || result.AttemptsRemaining == nil || *result.AttemptsRemaining != 3-attempt {
			t.Fatalf("attempt %d: %+v %v", attempt, result, err)
		}
		challenge.Version++
	}
	_, err = client.ChallengeAnswer(ctx, driverID, challenge.ID, challenge.Version, wrong, "fourth-wrong-key", nil)
	expectAPIError(t, err, "INVALID_STATE")
	created, err = client.ChallengeCreateTake(ctx, driverID, hold.ID, hold.Version, firstVehicleID, 1, "challenge-fresh-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := dataapi.DecodeAggregate[dataapi.Challenge](created)
	now = now.Add(5 * time.Minute)
	_, err = client.ChallengeAnswer(ctx, driverID, fresh.ID, fresh.Version, 0, "expired-answer-1", nil)
	expectAPIError(t, err, "CHALLENGE_EXPIRED")
	state, err := client.Checkout(ctx, driverID, hold.ID)
	if err != nil || state.IntentConfirmedAt != nil || state.Status != "holding" {
		t.Fatalf("expired challenge changed hold: %+v %v", state, err)
	}
}
