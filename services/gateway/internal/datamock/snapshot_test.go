package datamock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestSnapshotRestoresHoldAndIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock-state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	firstServer, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	firstClient := commandClient(t, firstServer)
	ctx := context.Background()
	created, err := firstClient.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "persistent-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot absent after success: %v", err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	secondClient := commandClient(t, restarted)
	state, err := secondClient.State(ctx, driverID)
	if err != nil || state.Checkout == nil || state.Checkout.ID != checkout.ID || state.NextStep == nil || *state.NextStep != "math" {
		t.Fatalf("state not restored: %+v %v", state, err)
	}
	ownerCheckout, err := secondClient.Checkout(ctx, driverID, checkout.ID)
	if err != nil || ownerCheckout.ID != checkout.ID {
		t.Fatalf("own checkout: %+v %v", ownerCheckout, err)
	}
	inspection, err := secondClient.Inspection(ctx, driverID, checkout.Inspection.ID)
	if err != nil || len(inspection.MissingSlots) != 8 {
		t.Fatalf("own inspection: %+v %v", inspection, err)
	}
	_, err = secondClient.Checkout(ctx, "8000000000000000002", checkout.ID)
	expectAPIError(t, err, "NOT_FOUND")
	_, err = secondClient.Inspection(ctx, "8000000000000000002", checkout.Inspection.ID)
	expectAPIError(t, err, "NOT_FOUND")
	adminCheckout, err := secondClient.Checkout(ctx, "8000000000000000003", checkout.ID)
	if err != nil || adminCheckout.ID != checkout.ID {
		t.Fatalf("admin checkout: %+v %v", adminCheckout, err)
	}
	vehicle, err := secondClient.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "holding" || vehicle.Version != 2 {
		t.Fatalf("hold lost on restart: %+v %v", vehicle, err)
	}
	replayed, err := secondClient.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "persistent-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	replayCheckout, err := dataapi.DecodeAggregate[dataapi.Checkout](replayed)
	if err != nil || replayCheckout.ID != checkout.ID {
		t.Fatalf("idempotency lost on restart: %+v %v", replayCheckout, err)
	}
	_, err = secondClient.CheckoutCancel(ctx, driverID, checkout.ID, checkout.Version, "persistent-cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	third, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	thirdClient := commandClient(t, third)
	state, err = thirdClient.State(ctx, driverID)
	if err != nil || state.Checkout != nil {
		t.Fatalf("cancelled checkout remains active: %+v %v", state, err)
	}
	vehicle, err = thirdClient.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" || vehicle.Version != 3 {
		t.Fatalf("cancellation lost on restart: %+v %v", vehicle, err)
	}
}

func TestFailedSnapshotNeverAcknowledgesHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock-state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	mock.saveSnapshot = func(stateSnapshot) error { return errors.New("synthetic disk failure") }
	client := commandClient(t, mock)
	_, err = client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "failed-save-key", nil)
	expectAPIError(t, err, "TEMPORARY_FAILURE")
	vehicle, err := client.Vehicle(context.Background(), driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" || vehicle.Version != 1 {
		t.Fatalf("failed save changed memory: %+v %v", vehicle, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed save created snapshot: %v", err)
	}
}

func TestCorruptSnapshotRefusesReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock-state.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithSnapshot("test-service-token", path, time.Now); err == nil {
		t.Fatal("corrupt snapshot silently reset to seed")
	}
}

func TestExpirationPersistsBeforeReadResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock-state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	if _, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "expiry-save-key", nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(15*time.Minute + time.Second)
	originalSave := mock.saveSnapshot
	mock.saveSnapshot = func(stateSnapshot) error { return errors.New("synthetic disk failure") }
	_, err = client.Vehicle(ctx, driverID, firstVehicleID)
	expectAPIError(t, err, "TEMPORARY_FAILURE")
	mock.mu.Lock()
	status := mock.vehicles[0].Status
	mock.mu.Unlock()
	if status != "holding" {
		t.Fatalf("failed expiration save changed memory: %s", status)
	}
	mock.saveSnapshot = originalSave
	vehicle, err := client.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("expiry not released: %+v %v", vehicle, err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	restartedClient := commandClient(t, restarted)
	vehicle, err = restartedClient.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("expiry lost on restart: %+v %v", vehicle, err)
	}
}
