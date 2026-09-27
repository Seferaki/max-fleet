package datamock

import (
	"context"
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
