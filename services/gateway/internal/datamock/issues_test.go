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
