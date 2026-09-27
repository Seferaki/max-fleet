package datamock

import (
	"context"
	"errors"
	"path/filepath"
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
	input := dataapi.IssueCreateInput{Category: "body_damage", Description: "Демонстрационное повреждение до выезда", InspectionID: &hold.Inspection.ID}
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
	if err != nil || stillHolding.Status != "holding" || len(mock.issues) != 0 {
		t.Fatalf("failed save changed issue state: %+v %v", stillHolding, err)
	}
	issued, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "before-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](issued)
	if err != nil || issue.Stage != "before" || issue.Status != "open" || !issue.BlocksIssuance {
		t.Fatalf("before issue: %+v %v", issue, err)
	}
	replayed, err := client.IssueCreate(ctx, driverID, firstVehicleID, 2, input, "before-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := dataapi.DecodeAggregate[dataapi.Issue](replayed)
	if same.ID != issue.ID {
		t.Fatal("duplicate issue created another record")
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
