package datamock

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestBeforeIssueCancelsHoldAndBlocksVehicle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "before-issue-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, _ := dataapi.DecodeAggregate[dataapi.Checkout](created)
	input := dataapi.IssueCreateInput{Category: "parking", Description: "Демонстрационная проблема с парковкой до выезда", InspectionID: &hold.Inspection.ID}
	_, err = client.IssueCreate(ctx, "8000000000000000002", firstVehicleID, 2, input, "foreign-issue-1", nil)
	expectAPIError(t, err, "NOT_FOUND")
	if _, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, dataapi.IssueCreateInput{Category: "body_damage", Description: "   ", InspectionID: &hold.Inspection.ID}, "empty-issue-1", nil); err == nil {
		t.Fatal("blank issue description accepted")
	}
	save := mock.saveSnapshot
	mock.saveSnapshot = func(stateSnapshot) error { return errors.New("injected issue save failure") }
	_, err = client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "failed-issue-1", nil)
	expectAPIError(t, err, "TEMPORARY_FAILURE")
	mock.saveSnapshot = save
	stillHolding, err := client.Checkout(ctx, driverID, hold.ID)
	if err != nil || stillHolding.Status != "holding" || len(mock.issues) != 0 || len(mock.notifications) != 0 {
		t.Fatalf("failed save changed issue state: %+v %v", stillHolding, err)
	}
	issued, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "before-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](issued)
	if err != nil || issue.Stage != "before" || issue.Category != "parking" || issue.Status != "open" || !issue.BlocksIssuance {
		t.Fatalf("before issue: %+v %v", issue, err)
	}
	if len(mock.notifications) != 1 {
		t.Fatalf("issue outbox count: %d", len(mock.notifications))
	}
	replayed, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "before-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := dataapi.DecodeAggregate[dataapi.Issue](replayed)
	if same.ID != issue.ID {
		t.Fatal("duplicate issue created another record")
	}
	if len(mock.notifications) != 1 {
		t.Fatal("duplicate issue created another notification")
	}
	checkout, err := client.Checkout(ctx, driverID, hold.ID)
	if err != nil || checkout.Status != "rejected" || checkout.Inspection.Status != "abandoned" {
		t.Fatalf("hold after issue: %+v %v", checkout, err)
	}
	vehicle, err := client.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "unavailable" || !vehicle.NeedsReview {
		t.Fatalf("vehicle after issue: %+v %v", vehicle, err)
	}
	_, err = client.Issue(ctx, "8000000000000000002", issue.ID)
	expectAPIError(t, err, "NOT_FOUND")
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client = commandClient(t, restarted)
	restored, err := client.Issue(ctx, driverID, issue.ID)
	if err != nil || restored.ID != issue.ID {
		t.Fatalf("issue after restart: %+v %v", restored, err)
	}
	_, err = client.CheckoutCreate(ctx, "8000000000000000002", firstVehicleID, vehicle.Version, "blocked-vehicle-1", nil)
	expectAPIError(t, err, "VEHICLE_UNAVAILABLE")
}

func TestDuringAndAfterIssuesKeepTripAndRequireReport(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tripID := "20000000-0000-4000-8000-000000000001"
	returnID := "30000000-0000-4000-8000-000000000001"
	inspectionID := "40000000-0000-4000-8000-000000000001"
	baseline := int64(12000)
	employee := mock.employees[driverID]
	employee.ActiveTripID = &tripID
	mock.employees[driverID] = employee
	mock.vehicles[0].Status = "in_trip"
	mock.vehicles[0].Version = 2
	mock.trips[tripID] = dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, Status: "active", BeforeInspection: dataapi.Inspection{OdometerKM: &baseline}, Issues: []dataapi.Issue{}, Version: 1, UpdatedAt: now}
	client := commandClient(t, mock)
	ctx := context.Background()
	_, err = client.IssueCreate(ctx, "8000000000000000002", firstVehicleID, 2, dataapi.IssueCreateInput{Category: "mechanical", Description: "Тестовое замечание", TripID: &tripID}, "foreign-trip-issue", nil)
	expectAPIError(t, err, "NOT_FOUND")
	result, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, dataapi.IssueCreateInput{Category: "mechanical", Description: "Тестовое замечание во время поездки", TripID: &tripID}, "during-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || issue.Stage != "during" || issue.TripID == nil || *issue.TripID != tripID {
		t.Fatalf("during issue: %+v %v", issue, err)
	}
	active, err := client.Trip(ctx, driverID, tripID)
	if err != nil || active.Status != "active" || len(active.Issues) != 1 || mock.vehicles[0].Status != "in_trip" || !mock.vehicles[0].NeedsReview {
		t.Fatalf("during issue changed trip: %+v %v", active, err)
	}
	active.Status = "returning"
	active.ReturnID = &returnID
	active.Version++
	mock.trips[tripID] = active
	fuel, odometer := 50, int64(12025)
	noDamage, dirty, safe := false, false, true
	mock.returns[returnID] = dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", IntentConfirmedAt: &now, ParkingLocation: &dataapi.ParkingLocation{ID: "50000000-0000-4000-8000-000000000001", Latitude: 55.75, Longitude: 37.62, Source: "manual_map", ConfirmedAt: now}, Version: 1, UpdatedAt: now,
		Inspection: dataapi.Inspection{ID: inspectionID, Phase: "after", Status: "draft", FuelLevel: &fuel, OdometerKM: &odometer, NewDamage: &noDamage, CabinClean: &dirty, ParkingAllowed: &safe, KeysReturned: &safe, CarLocked: &safe, OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, MissingSlots: []int{}, PhotosConfirmedAt: &now, Version: 1, UpdatedAt: now}}
	_, err = client.ReturnComplete(ctx, driverID, returnID, 1, "dirty-unreported", nil)
	expectAPIError(t, err, "INVALID_STATE")
	result, err = client.IssueCreate(ctx, driverID, firstVehicleID, 3, dataapi.IssueCreateInput{Category: "cleanliness", Description: "Тест: грязный салон", InspectionID: &inspectionID}, "after-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || issue.Stage != "after" || issue.InspectionID == nil || *issue.InspectionID != inspectionID || issue.TripID == nil || *issue.TripID != tripID {
		t.Fatalf("after issue: %+v %v", issue, err)
	}
	completed, err := client.ReturnComplete(ctx, driverID, returnID, 2, "dirty-complete", nil)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := dataapi.DecodeAggregate[dataapi.Return](completed)
	if err != nil || finish.Status != "completed" || mock.vehicles[0].Status != "unavailable" || !mock.vehicles[0].NeedsReview || len(mock.trips[tripID].Issues) != 2 {
		t.Fatalf("dirty return released vehicle: %+v %v", finish, err)
	}
}

func TestPostReturnIssueKeepsCompletedTripAndBlocksHeldVehicle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tripID := "20000000-0000-4000-8000-000000000001"
	beforeID := "40000000-0000-4000-8000-000000000001"
	afterID := "40000000-0000-4000-8000-000000000002"
	employee := mock.employees[driverID]
	finished := dataapi.Trip{
		ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID,
		Status: "completed", BeforeInspection: dataapi.Inspection{ID: beforeID, Phase: "before", Status: "finalized", Version: 2},
		AfterInspection: &dataapi.Inspection{ID: afterID, Phase: "after", Status: "finalized", Version: 3},
		Issues:          []dataapi.Issue{}, Version: 4, UpdatedAt: now,
	}
	mock.trips[tripID] = finished
	client := commandClient(t, mock)
	ctx := context.Background()
	other := "8000000000000000002"
	heldResult, err := client.CheckoutCreate(ctx, other, firstVehicleID, 1, "later-driver-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	held, err := dataapi.DecodeAggregate[dataapi.Checkout](heldResult)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := client.StageIssueAsset(ctx, driverID, dataapi.IssueStageInput{
		ScopeType: "trip", ScopeID: tripID, SourceEventKey: "post-return-photo", IdempotencyKey: "post-return-stage", ContentType: "image/png", Image: syntheticPNG(t, 71),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := dataapi.IssueCreateInput{Category: "car_lock", Description: "После поездки машина не закрывается", TripID: &tripID, AssetIDs: []string{asset.AssetID}}
	_, err = client.IssueCreate(ctx, other, firstVehicleID, 2, input, "foreign-post-return", nil)
	expectAPIError(t, err, "NOT_FOUND")
	_, err = client.IssueCreate(ctx, driverID, firstVehicleID, 3, input, "stale-post-return", nil)
	expectAPIError(t, err, "STALE_VERSION")
	save := mock.saveSnapshot
	mock.saveSnapshot = func(stateSnapshot) error { return errors.New("injected post-return save failure") }
	_, err = client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "failed-post-return", nil)
	expectAPIError(t, err, "TEMPORARY_FAILURE")
	mock.saveSnapshot = save
	if len(mock.issues) != 0 || len(mock.notifications) != 0 || !reflect.DeepEqual(mock.trips[tripID], finished) {
		t.Fatal("failed save changed completed trip, issues or outbox")
	}
	result, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "post-return-issue", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || issue.Stage != "post_return" || issue.Category != "car_lock" || issue.TripID == nil || *issue.TripID != tripID || issue.InspectionID != nil || len(issue.AssetIDs) != 1 {
		t.Fatalf("post-return issue: %+v %v", issue, err)
	}
	if !reflect.DeepEqual(mock.trips[tripID], finished) || mock.vehicles[0].Status != "unavailable" || !mock.vehicles[0].NeedsReview || len(mock.notifications) != 1 {
		t.Fatal("post-return changed snapshot or failed to block and notify")
	}
	currentHold, err := client.Checkout(ctx, other, held.ID)
	if err != nil || currentHold.Status != "rejected" {
		t.Fatalf("later hold remained active: %+v %v", currentHold, err)
	}
	projected, err := client.Trip(ctx, driverID, tripID)
	if err != nil || projected.Version != finished.Version || len(projected.Issues) != 1 || projected.Issues[0].ID != issue.ID {
		t.Fatalf("history projection: %+v %v", projected, err)
	}
	_, err = client.Trip(ctx, other, tripID)
	expectAPIError(t, err, "NOT_FOUND")
	replay, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "post-return-issue", nil)
	if err != nil || replay.Operation != "issue.create" || len(mock.issues) != 1 || len(mock.notifications) != 1 {
		t.Fatalf("idempotent replay: %+v %v", replay, err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	restored, err := commandClient(t, restarted).Trip(ctx, driverID, tripID)
	if err != nil || restored.Version != finished.Version || len(restored.Issues) != 1 || !reflect.DeepEqual(restarted.trips[tripID], finished) {
		t.Fatalf("restart projection: %+v %v", restored, err)
	}
}
