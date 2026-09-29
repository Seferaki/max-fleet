package datamock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestBE12SyntheticLifecycleUnknownAccessTakeIssueReturnHistoryAdmin(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", filepath.Join(t.TempDir(), "state.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	employeeMaxID := "8000000000000000088"
	displayName := "Синтетический водитель"

	if _, err := client.Vehicles(ctx, employeeMaxID, dataapi.VehicleFilter{}); err == nil {
		t.Fatal("unknown MAX actor could read the fleet")
	} else {
		var apiErr *dataapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != "ACCESS_DENIED" {
			t.Fatalf("unknown actor error = %v", err)
		}
	}

	grantIntent := dataapi.AdminChallengeIntent{
		Operation: "employee.grant", MaxUserID: &employeeMaxID, DisplayName: &displayName,
	}
	grantProof := solvedAdminProof(t, client, "employee_grant", grantIntent, "be12-grant")
	if _, err := client.EmployeeGrant(ctx, adminActorID, employeeMaxID, displayName, grantProof, "be12-employee-grant", nil); err != nil {
		t.Fatalf("grant access: %v", err)
	}
	me, err := client.Me(ctx, employeeMaxID)
	if err != nil || !me.Allowed || me.Employee == nil || !me.Employee.CanStartTrip {
		t.Fatalf("granted employee access: %+v %v", me, err)
	}
	if _, err := client.AdminSummary(ctx, employeeMaxID); err == nil {
		t.Fatal("employee read an admin endpoint")
	} else {
		var apiErr *dataapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "ACCESS_DENIED" {
			t.Fatalf("employee admin-read error = %v", err)
		}
	}

	available := true
	vehicles, err := client.Vehicles(ctx, employeeMaxID, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(vehicles.Items) == 0 {
		t.Fatalf("newly granted employee cannot select a vehicle: %+v %v", vehicles, err)
	}
	vehicle := vehicles.Items[0]
	createdHold, err := client.CheckoutCreate(ctx, employeeMaxID, vehicle.ID, vehicle.Version, "be12-checkout-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](createdHold)
	if err != nil || !hold.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("hold must expire in 15 minutes: %+v %v", hold, err)
	}
	takeChallengeResult, err := client.ChallengeCreateTake(ctx, employeeMaxID, hold.ID, hold.Version, vehicle.ID, vehicle.Version, "be12-take-challenge", nil)
	if err != nil {
		t.Fatal(err)
	}
	takeChallenge, err := dataapi.DecodeAggregate[dataapi.Challenge](takeChallengeResult)
	if err != nil {
		t.Fatal(err)
	}
	takeAnswer, _ := challengeChoices(t, takeChallenge)
	if _, err := client.ChallengeAnswer(ctx, employeeMaxID, takeChallenge.ID, takeChallenge.Version, takeAnswer, "be12-take-answer", nil); err != nil {
		t.Fatalf("take challenge: %v", err)
	}
	rules, err := client.CurrentRules(ctx, employeeMaxID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.State(ctx, employeeMaxID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout after challenge: %+v %v", state, err)
	}
	if _, err := client.CheckoutAcceptRules(ctx, employeeMaxID, hold.ID, state.Checkout.Version, rules.ID, "be12-accept-rules", nil); err != nil {
		t.Fatalf("accept rules: %v", err)
	}
	state, err = client.State(ctx, employeeMaxID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout after rules: %+v %v", state, err)
	}
	fuelBefore, odometerBefore, noDamage := 75, int64(12001), false
	updated, err := client.InspectionUpdate(ctx, employeeMaxID, state.Checkout.Inspection.ID, state.Checkout.Inspection.Version,
		dataapi.InspectionUpdateInput{FuelLevel: &fuelBefore, OdometerKM: &odometerBefore, NewDamage: &noDamage}, "be12-before-fields", nil)
	if err != nil {
		t.Fatalf("before inspection: %v", err)
	}
	beforeInspection, err := dataapi.DecodeAggregate[dataapi.Inspection](updated)
	if err != nil {
		t.Fatal(err)
	}
	for slot := 1; slot <= 8; slot++ {
		key := fmt.Sprintf("be12-before-photo-%d", slot)
		uploaded, err := client.UploadInspectionPhoto(ctx, employeeMaxID, dataapi.InspectionPhotoInput{
			InspectionID: beforeInspection.ID, Slot: slot, Version: beforeInspection.Version,
			SourceEventKey: key, IdempotencyKey: key, ContentType: "image/png", Image: syntheticPNG(t, uint8(slot)),
		})
		if err != nil {
			t.Fatalf("before photo %d/8: %v", slot, err)
		}
		beforeInspection = uploaded.Inspection
	}
	if _, err := client.InspectionConfirmPhotos(ctx, employeeMaxID, beforeInspection.ID, beforeInspection.Version, "be12-before-confirm-photos", nil); err != nil {
		t.Fatalf("confirm before photos: %v", err)
	}
	state, err = client.State(ctx, employeeMaxID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	if _, err := client.CheckoutSetNoNewIssues(ctx, employeeMaxID, hold.ID, state.Checkout.Version, "be12-no-before-issues", nil); err != nil {
		t.Fatalf("record before issue answer: %v", err)
	}
	state, err = client.State(ctx, employeeMaxID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout before start: %+v %v", state, err)
	}
	started, err := client.CheckoutStart(ctx, employeeMaxID, hold.ID, state.Checkout.Version, "be12-checkout-start", nil)
	if err != nil {
		t.Fatalf("start trip: %v", err)
	}
	trip, err := dataapi.DecodeAggregate[dataapi.Trip](started)
	if err != nil || trip.Status != "active" || len(trip.BeforeInspection.OccupiedSlots) != 8 {
		t.Fatalf("started trip does not contain the 8-photo before set: %+v %v", trip, err)
	}

	currentVehicle, err := client.Vehicle(ctx, employeeMaxID, vehicle.ID)
	if err != nil {
		t.Fatal(err)
	}
	tripID := trip.ID
	issueResult, err := client.IssueCreate(ctx, employeeMaxID, vehicle.ID, currentVehicle.Version, dataapi.IssueCreateInput{
		Category: "car_lock", Description: "Демонстрационная проверка замка", TripID: &tripID, AssetIDs: []string{},
	}, "be12-issue-create", nil)
	if err != nil {
		t.Fatalf("create trip issue: %v", err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](issueResult)
	if err != nil || issue.Category != "car_lock" || issue.TripID == nil || *issue.TripID != trip.ID {
		t.Fatalf("trip issue: %+v %v", issue, err)
	}
	trip, err = client.Trip(ctx, employeeMaxID, trip.ID)
	if err != nil || trip.Status != "active" {
		t.Fatalf("trip after issue report: %+v %v", trip, err)
	}
	activeTripVersion := trip.Version
	beginReturn, err := client.TripBeginReturn(ctx, employeeMaxID, trip.ID, trip.Version, "be12-begin-return", nil)
	if err != nil {
		t.Fatalf("begin return: %v", err)
	}
	returnDraft, err := dataapi.DecodeAggregate[dataapi.Return](beginReturn)
	if err != nil || returnDraft.Step != "math" {
		t.Fatalf("return draft: %+v %v", returnDraft, err)
	}
	returnChallengeResult, err := client.ChallengeCreateReturn(ctx, employeeMaxID, returnDraft.ID, returnDraft.Version, trip.ID, activeTripVersion, "be12-return-challenge", nil)
	if err != nil {
		t.Fatalf("create return challenge: %v", err)
	}
	returnChallenge, err := dataapi.DecodeAggregate[dataapi.Challenge](returnChallengeResult)
	if err != nil {
		t.Fatal(err)
	}
	returnAnswer, _ := challengeChoices(t, returnChallenge)
	if _, err := client.ChallengeAnswer(ctx, employeeMaxID, returnChallenge.ID, returnChallenge.Version, returnAnswer, "be12-return-answer", nil); err != nil {
		t.Fatalf("return challenge: %v", err)
	}
	state, err = client.State(ctx, employeeMaxID)
	if err != nil || state.Return == nil {
		t.Fatalf("return checklist: %+v %v", state, err)
	}
	ret := *state.Return
	fuelAfter, odometerAfter := 50, int64(12002)
	clean, parkingAllowed, keysReturned, carLocked := true, true, true, true
	updated, err = client.InspectionUpdate(ctx, employeeMaxID, ret.Inspection.ID, ret.Inspection.Version, dataapi.InspectionUpdateInput{
		FuelLevel: &fuelAfter, OdometerKM: &odometerAfter, NewDamage: &noDamage, CabinClean: &clean,
		ParkingAllowed: &parkingAllowed, KeysReturned: &keysReturned, CarLocked: &carLocked,
	}, "be12-return-checklist", nil)
	if err != nil {
		t.Fatalf("return checklist update: %v", err)
	}
	afterInspection, err := dataapi.DecodeAggregate[dataapi.Inspection](updated)
	if err != nil {
		t.Fatal(err)
	}
	for slot := 1; slot <= 8; slot++ {
		key := fmt.Sprintf("be12-after-photo-%d", slot)
		uploaded, err := client.UploadInspectionPhoto(ctx, employeeMaxID, dataapi.InspectionPhotoInput{
			InspectionID: afterInspection.ID, Slot: slot, Version: afterInspection.Version,
			SourceEventKey: key, IdempotencyKey: key, ContentType: "image/png", Image: syntheticPNG(t, uint8(16+slot)),
		})
		if err != nil {
			t.Fatalf("after photo %d/8: %v", slot, err)
		}
		afterInspection = uploaded.Inspection
	}
	if _, err := client.InspectionConfirmPhotos(ctx, employeeMaxID, afterInspection.ID, afterInspection.Version, "be12-after-confirm-photos", nil); err != nil {
		t.Fatalf("confirm after photos: %v", err)
	}
	state, err = client.State(ctx, employeeMaxID)
	if err != nil || state.Return == nil {
		t.Fatalf("return after photos: %+v %v", state, err)
	}
	ret = *state.Return
	if _, err := client.ReturnSetLocation(ctx, employeeMaxID, ret.ID, ret.Version, "be12-manual-parking", nil,
		dataapi.LocationInput{Latitude: 55.751, Longitude: 37.621, Source: "manual_map", Confirmed: true}); err != nil {
		t.Fatalf("manual parking point: %v", err)
	}
	state, err = client.State(ctx, employeeMaxID)
	if err != nil || state.Return == nil {
		t.Fatalf("return before completion: %+v %v", state, err)
	}
	completedReturnResult, err := client.ReturnComplete(ctx, employeeMaxID, state.Return.ID, state.Return.Version, "be12-return-complete", nil)
	if err != nil {
		t.Fatalf("complete return: %v", err)
	}
	completedReturn, err := dataapi.DecodeAggregate[dataapi.Return](completedReturnResult)
	if err != nil || completedReturn.Status != "completed" || completedReturn.ParkingLocation == nil || completedReturn.ParkingLocation.Source != "manual_map" || len(completedReturn.Inspection.OccupiedSlots) != 8 {
		t.Fatalf("completed Return aggregate: %+v %v", completedReturn, err)
	}
	completedTrip, err := client.Trip(ctx, employeeMaxID, trip.ID)
	if err != nil || completedTrip.Status != "completed" || completedTrip.AfterInspection == nil || len(completedTrip.BeforeInspection.OccupiedSlots) != 8 || len(completedTrip.AfterInspection.OccupiedSlots) != 8 || completedTrip.ParkingLocation == nil || completedTrip.ParkingLocation.Source != "manual_map" {
		t.Fatalf("completed history snapshot: %+v %v", completedTrip, err)
	}
	myTrips, err := client.MyTrips(ctx, employeeMaxID, 5, "")
	if err != nil || len(myTrips.Items) != 1 || myTrips.Items[0].ID != trip.ID || len(myTrips.Items[0].Issues) != 1 {
		t.Fatalf("owner history: %+v %v", myTrips, err)
	}

	adminMe, err := client.Me(ctx, adminActorID)
	if err != nil || adminMe.Employee == nil || adminMe.Employee.Role != "admin" {
		t.Fatalf("admin identity: %+v %v", adminMe, err)
	}
	adminTrips, err := client.AdminTrips(ctx, adminActorID, dataapi.AdminTripFilter{State: "completed", EmployeeID: me.Employee.ID, VehicleID: vehicle.ID, Limit: 5})
	if err != nil || len(adminTrips.Items) != 1 || adminTrips.Items[0].ID != trip.ID {
		t.Fatalf("admin trip history: %+v %v", adminTrips, err)
	}
	openIssues, err := client.AdminIssues(ctx, adminActorID, dataapi.AdminIssueFilter{Status: "open", VehicleID: vehicle.ID, Limit: 5})
	if err != nil || len(openIssues.Items) != 1 || openIssues.Items[0].ID != issue.ID {
		t.Fatalf("admin issue queue: %+v %v", openIssues, err)
	}
	assignedResult, err := client.IssueResolve(ctx, adminActorID, issue.ID, issue.Version, dataapi.IssueResolveInput{Status: "in_progress"}, "be12-issue-assign", nil)
	if err != nil {
		t.Fatalf("admin take-work: %v", err)
	}
	assignedIssue, err := dataapi.DecodeAggregate[dataapi.Issue](assignedResult)
	if err != nil || assignedIssue.Status != "in_progress" || assignedIssue.AssignedTo == nil || *assignedIssue.AssignedTo != adminMe.Employee.ID {
		t.Fatalf("admin assignment audit: %+v %v", assignedIssue, err)
	}
	resolvedResult, err := client.IssueResolve(ctx, adminActorID, assignedIssue.ID, assignedIssue.Version,
		dataapi.IssueResolveInput{Status: "resolved", Comment: "Замок проверен в синтетическом сценарии", Confirmation: true}, "be12-issue-resolve", nil)
	if err != nil {
		t.Fatalf("admin resolve: %v", err)
	}
	resolvedIssue, err := dataapi.DecodeAggregate[dataapi.Issue](resolvedResult)
	if err != nil || resolvedIssue.Status != "resolved" || resolvedIssue.AssignedTo == nil || *resolvedIssue.AssignedTo != adminMe.Employee.ID {
		t.Fatalf("resolved issue lost assignment audit: %+v %v", resolvedIssue, err)
	}
	finalTrip, err := client.Trip(ctx, employeeMaxID, trip.ID)
	if err != nil || finalTrip.Status != "completed" || len(finalTrip.Issues) != 1 || finalTrip.Issues[0].Status != "resolved" || finalTrip.Issues[0].AssignedTo == nil || *finalTrip.Issues[0].AssignedTo != adminMe.Employee.ID {
		t.Fatalf("final immutable history/admin projection: %+v %v", finalTrip, err)
	}
}
