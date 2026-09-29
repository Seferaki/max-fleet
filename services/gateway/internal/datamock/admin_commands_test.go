package datamock

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const secondAdminID = "8000000000000000006"

func solvedAdminProof(t *testing.T, client *dataapi.Client, purpose string, intent dataapi.AdminChallengeIntent, suffix string) string {
	t.Helper()
	ctx := context.Background()
	created, err := client.AdminChallengeCreate(ctx, adminActorID, purpose, intent, "admin-proof-create-"+suffix, nil)
	if err != nil {
		t.Fatalf("create %s challenge: %v", purpose, err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](created)
	if err != nil {
		t.Fatalf("decode %s challenge: %v", purpose, err)
	}
	correct, _ := challengeChoices(t, challenge)
	answered, err := client.ChallengeAnswer(ctx, adminActorID, challenge.ID, challenge.Version, correct, "admin-proof-answer-"+suffix, nil)
	if err != nil || answered.ChallengeProofID == nil || *answered.ChallengeProofID != challenge.ID {
		t.Fatalf("answer %s challenge: result=%+v err=%v", purpose, answered, err)
	}
	return challenge.ID
}

func TestVehicleAdminCommandsBindActorIntentAndConsumeProofOnce(t *testing.T) {
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	secondAdmin := mock.employees[adminActorID]
	secondAdmin.ID = "80000000-0000-4000-8000-000000000006"
	secondAdmin.MaxUserID = secondAdminID
	mock.employees[secondAdminID] = secondAdmin
	client := commandClient(t, mock)
	ctx := context.Background()
	reason := "Диагностика тормозов"
	version := int64(1)
	intent := dataapi.AdminChallengeIntent{Operation: "vehicle.block", TargetID: stringPointer(firstVehicleID), ExpectedVersion: &version, Reason: &reason}
	proofID := solvedAdminProof(t, client, "vehicle_block", intent, "vehicle-block")

	status, result, code := postRawCommand(t, mock, adminActorID, "block-tampered", "vehicle.block", firstVehicleID, 1, map[string]any{"reason": "Другая причина", "challenge_id": proofID})
	if status != http.StatusUnprocessableEntity || code != "CHALLENGE_INVALID" || result != nil {
		t.Fatalf("tampered block intent: status=%d code=%s result=%+v", status, code, result)
	}
	status, result, code = postRawCommand(t, mock, secondAdminID, "block-foreign-actor", "vehicle.block", firstVehicleID, 1, map[string]any{"reason": reason, "challenge_id": proofID})
	if status != http.StatusUnprocessableEntity || code != "CHALLENGE_INVALID" || result != nil {
		t.Fatalf("foreign admin actor: status=%d code=%s result=%+v", status, code, result)
	}
	blockedResult, err := client.VehicleBlock(ctx, adminActorID, firstVehicleID, 1, reason, proofID, "block-correct", nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := dataapi.DecodeAggregate[dataapi.Vehicle](blockedResult)
	if err != nil || !blocked.ManualBlocked || blocked.Status != "unavailable" || blocked.Version != 2 {
		t.Fatalf("block result: %+v %v", blocked, err)
	}
	replayed, err := client.VehicleBlock(ctx, adminActorID, firstVehicleID, 1, reason, proofID, "block-correct", nil)
	if err != nil || replayed.Operation != blockedResult.Operation || string(replayed.Aggregate) != string(blockedResult.Aggregate) {
		t.Fatalf("idempotent block replay: %+v %v", replayed, err)
	}
	_, err = client.VehicleBlock(ctx, adminActorID, firstVehicleID, 2, reason, proofID, "block-proof-reuse", nil)
	expectAPIError(t, err, "CHALLENGE_INVALID")

	unblockReason := "Диагностика завершена"
	unblockVersion := int64(2)
	reviewCompleted := true
	unblockIntent := dataapi.AdminChallengeIntent{Operation: "vehicle.unblock", TargetID: stringPointer(firstVehicleID), ExpectedVersion: &unblockVersion, Reason: &unblockReason, ReviewCompleted: &reviewCompleted}
	unblockProof := solvedAdminProof(t, client, "vehicle_unblock", unblockIntent, "vehicle-unblock")
	issueID := "40000000-0000-4000-8000-000000000001"
	mock.issues[issueID] = dataapi.Issue{ID: issueID, VehicleID: firstVehicleID, AuthorID: driverID, Stage: "after", Category: "mechanical", Description: "Требует проверки", Status: "open", BlocksIssuance: true, Version: 1, UpdatedAt: clock}
	_, err = client.VehicleUnblock(ctx, adminActorID, firstVehicleID, 2, unblockReason, unblockProof, true, "unblock-blocked-by-issue", nil)
	expectAPIError(t, err, "INVALID_STATE")
	if mock.challenges[unblockProof].ProofConsumed {
		t.Fatal("failed unblock consumed its challenge proof")
	}
	issue := mock.issues[issueID]
	issue.Status = "resolved"
	mock.issues[issueID] = issue
	unblockedResult, err := client.VehicleUnblock(ctx, adminActorID, firstVehicleID, 2, unblockReason, unblockProof, true, "unblock-blocked-by-issue", nil)
	if err != nil {
		t.Fatal(err)
	}
	unblocked, err := dataapi.DecodeAggregate[dataapi.Vehicle](unblockedResult)
	if err != nil || unblocked.ManualBlocked || unblocked.NeedsReview || unblocked.Status != "available" || unblocked.Version != 3 {
		t.Fatalf("unblock result: %+v %v", unblocked, err)
	}
}

func TestEmployeeGrantUsesNullTargetAndConsumesProofOnce(t *testing.T) {
	mock, err := NewWithClock("test-service-token", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	maxUserID, displayName := "8000000000000000099", "Новый сотрудник"
	intent := dataapi.AdminChallengeIntent{Operation: "employee.grant", MaxUserID: &maxUserID, DisplayName: &displayName}
	proofID := solvedAdminProof(t, client, "employee_grant", intent, "employee-grant")
	status, result, code := postRawCommand(t, mock, adminActorID, "grant-tampered", "employee.grant", nil, nil, map[string]any{"max_user_id": maxUserID, "display_name": "Подменённое имя", "challenge_id": proofID})
	if status != http.StatusUnprocessableEntity || code != "CHALLENGE_INVALID" || result != nil {
		t.Fatalf("tampered grant intent: status=%d code=%s result=%+v", status, code, result)
	}
	createdResult, err := client.EmployeeGrant(context.Background(), adminActorID, maxUserID, displayName, proofID, "grant-correct", nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := dataapi.DecodeAggregate[dataapi.Employee](createdResult)
	if err != nil || created.MaxUserID != maxUserID || created.DisplayName != displayName || created.Role != "employee" || !created.CanStartTrip || created.Version != 1 {
		t.Fatalf("grant result: %+v %v", created, err)
	}
	replayed, err := client.EmployeeGrant(context.Background(), adminActorID, maxUserID, displayName, proofID, "grant-correct", nil)
	if err != nil || string(replayed.Aggregate) != string(createdResult.Aggregate) {
		t.Fatalf("idempotent grant replay: %+v %v", replayed, err)
	}
	_, err = client.EmployeeGrant(context.Background(), adminActorID, maxUserID, displayName, proofID, "grant-proof-reuse", nil)
	expectAPIError(t, err, "CHALLENGE_INVALID")
}

func TestAdminProofConcurrentUseHasOneWinner(t *testing.T) {
	mock, err := NewWithClock("test-service-token", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	reason := "Параллельная блокировка"
	version := int64(1)
	intent := dataapi.AdminChallengeIntent{Operation: "vehicle.block", TargetID: stringPointer(firstVehicleID), ExpectedVersion: &version, Reason: &reason}
	proofID := solvedAdminProof(t, client, "vehicle_block", intent, "vehicle-block-race")
	type outcome struct{ err error }
	results := make(chan outcome, 2)
	for _, key := range []string{"block-race-a", "block-race-b"} {
		go func(key string) {
			_, err := client.VehicleBlock(context.Background(), adminActorID, firstVehicleID, version, reason, proofID, key, nil)
			results <- outcome{err: err}
		}(key)
	}
	wins := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			wins++
			continue
		}
		var apiErr *dataapi.APIError
		if !errors.As(result.err, &apiErr) || apiErr.Code != "STALE_VERSION" {
			t.Fatalf("parallel proof use returned unexpected error: %v", result.err)
		}
	}
	vehicle, err := client.Vehicle(context.Background(), adminActorID, firstVehicleID)
	if err != nil || wins != 1 || !vehicle.ManualBlocked || vehicle.Version != 2 {
		t.Fatalf("parallel proof use: winners=%d vehicle=%+v err=%v", wins, vehicle, err)
	}
}

func TestEmployeeAccessRequiresAdminAndReleasesCancelledHold(t *testing.T) {
	mock, err := NewWithClock("test-service-token", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	createdResult, err := client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "access-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](createdResult)
	if err != nil {
		t.Fatal(err)
	}
	employee := mock.employees[driverID]
	version, canStart := employee.Version, false
	reason := "Сверка с руководителем"
	intent := dataapi.AdminChallengeIntent{Operation: "employee.access", TargetID: stringPointer(employee.ID), ExpectedVersion: &version, CanStartTrip: &canStart, Reason: &reason}
	proofID := solvedAdminProof(t, client, "employee_access", intent, "employee-access")
	status, result, code := postRawCommand(t, mock, driverID, "access-nonadmin", "employee.access", employee.ID, version, map[string]any{"can_start_trip": false, "reason": reason, "challenge_id": proofID})
	if status != http.StatusForbidden || code != "ADMIN_REQUIRED" || result != nil {
		t.Fatalf("non-admin access change: status=%d code=%s result=%+v", status, code, result)
	}
	changedResult, err := client.EmployeeAccess(context.Background(), adminActorID, employee.ID, version, false, reason, proofID, "access-correct", nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := dataapi.DecodeAggregate[dataapi.Employee](changedResult)
	if err != nil || changed.CanStartTrip || changed.Version != version+1 {
		t.Fatalf("access result: %+v %v", changed, err)
	}
	if got := mock.checkouts[checkout.ID]; got.Status != "cancelled" || got.Inspection.Status != "abandoned" {
		t.Fatalf("employee hold was not safely cancelled: %+v", got)
	}
	vehicle, err := client.Vehicle(context.Background(), driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" || vehicle.Version != 3 {
		t.Fatalf("cancelled hold did not release vehicle: %+v %v", vehicle, err)
	}
}

func TestTripAdminClosePreservesMissingDataAndDoesNotRollBackOdometer(t *testing.T) {
	clock := time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	tripID := "50000000-0000-4000-8000-000000000001"
	employee := mock.employees[driverID]
	employee.ActiveTripID = stringPointer(tripID)
	mock.employees[driverID] = employee
	vehicleIndex := mock.vehicleIndex(firstVehicleID)
	vehicle := mock.vehicles[vehicleIndex]
	vehicle.Status = "in_trip"
	vehicle.Version = 7
	odometer := int64(42150)
	vehicle.CurrentOdometerKM = &odometer
	mock.vehicles[vehicleIndex] = vehicle
	mock.trips[tripID] = dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, CheckoutID: "60000000-0000-4000-8000-000000000001", Status: "active", StartedAt: clock.Add(-time.Hour), Version: 1, UpdatedAt: clock, MissingData: []string{}}

	reason := "Водитель недоступен"
	version := int64(1)
	latitude, longitude := 55.75, 37.61
	fuel, lowerOdometer := 75, int64(42149)
	keysReturned, carLocked := false, false
	landmark := "У ворот"
	available := &dataapi.AdminCloseData{FuelLevel: &fuel, OdometerKM: &lowerOdometer, KeysReturned: &keysReturned, CarLocked: &carLocked, Latitude: &latitude, Longitude: &longitude, Landmark: &landmark}
	intent := dataapi.AdminChallengeIntent{Operation: "trip.admin_close", TargetID: stringPointer(tripID), ExpectedVersion: &version, Reason: &reason, AvailableData: available}
	proofID := solvedAdminProof(t, client, "admin_close", intent, "trip-admin-close")
	partial := &dataapi.AdminCloseData{Latitude: &latitude}
	if _, err := client.TripAdminClose(context.Background(), adminActorID, tripID, 1, reason, proofID, partial, "admin-close-partial", nil); err == nil {
		t.Fatal("half coordinate pair was accepted")
	}
	if mock.challenges[proofID].ProofConsumed {
		t.Fatal("invalid admin close input consumed proof")
	}
	changedFuel := 50
	tampered := map[string]any{"reason": reason, "challenge_id": proofID, "available_data": map[string]any{"fuel_level": changedFuel, "odometer_km": lowerOdometer, "keys_returned": keysReturned, "car_locked": carLocked, "latitude": latitude, "longitude": longitude, "landmark": landmark}}
	status, result, code := postRawCommand(t, mock, adminActorID, "admin-close-tampered-data", "trip.admin_close", tripID, version, tampered)
	if status != http.StatusUnprocessableEntity || code != "CHALLENGE_INVALID" || result != nil || mock.challenges[proofID].ProofConsumed {
		t.Fatalf("tampered available_data: status=%d code=%s result=%+v consumed=%v", status, code, result, mock.challenges[proofID].ProofConsumed)
	}
	closedResult, err := client.TripAdminClose(context.Background(), adminActorID, tripID, 1, reason, proofID, available, "admin-close-correct", nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := dataapi.DecodeAggregate[dataapi.Trip](closedResult)
	if err != nil || closed.Status != "closed_by_admin" || closed.ReturnID == nil || closed.AfterInspection == nil || closed.AfterInspection.Status != "abandoned" {
		t.Fatalf("admin close result: %+v %v", closed, err)
	}
	if len(closed.MissingData) != 1 || closed.MissingData[0] != "after_photos" {
		t.Fatalf("admin close concealed missing data: %v", closed.MissingData)
	}
	returnDraft := mock.returns[*closed.ReturnID]
	if returnDraft.Status != "admin_closed" || returnDraft.Inspection.Status != "abandoned" {
		t.Fatalf("admin close return draft: %+v", returnDraft)
	}
	updatedEmployee := mock.employees[driverID]
	if updatedEmployee.ActiveTripID != nil {
		t.Fatalf("admin close left active trip assigned: %+v", updatedEmployee)
	}
	updatedVehicle := mock.vehicles[vehicleIndex]
	if updatedVehicle.Status != "unavailable" || !updatedVehicle.NeedsReview || updatedVehicle.CurrentOdometerKM == nil || *updatedVehicle.CurrentOdometerKM != odometer || updatedVehicle.CurrentFuel == nil || *updatedVehicle.CurrentFuel != fuel {
		t.Fatalf("admin close vehicle snapshot: %+v", updatedVehicle)
	}
	replayed, err := client.TripAdminClose(context.Background(), adminActorID, tripID, 1, reason, proofID, available, "admin-close-correct", nil)
	if err != nil || string(replayed.Aggregate) != string(closedResult.Aggregate) {
		t.Fatalf("idempotent admin close replay: %+v %v", replayed, err)
	}
	_, err = client.TripAdminClose(context.Background(), adminActorID, tripID, closed.Version, reason, proofID, available, "admin-close-proof-reuse", nil)
	expectAPIError(t, err, "CHALLENGE_INVALID")
}

func stringPointer(value string) *string { return &value }
