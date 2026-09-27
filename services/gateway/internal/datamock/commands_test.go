package datamock

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const firstVehicleID = "10000000-0000-4000-8000-000000000001"

func commandClient(t *testing.T, mock *Server) *dataapi.Client {
	t.Helper()
	server := httptest.NewServer(mock.Handler())
	t.Cleanup(server.Close)
	client, err := dataapi.New(dataapi.Config{BaseURL: server.URL + "/internal/v1", Token: "test-service-token"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func expectAPIError(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestCheckoutHoldIdempotencyAndCancel(t *testing.T) {
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	first, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "create-key-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](first)
	if err != nil || checkout.Status != "holding" || checkout.ExpiresAt.Sub(clock) != 15*time.Minute || len(checkout.Inspection.MissingSlots) != 8 {
		t.Fatalf("hold: %+v %v", checkout, err)
	}
	again, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "create-key-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := dataapi.DecodeAggregate[dataapi.Checkout](again)
	if err != nil || duplicate.ID != checkout.ID {
		t.Fatalf("duplicate created another hold: %+v %v", duplicate, err)
	}
	_, err = client.CheckoutCreate(ctx, driverID, "10000000-0000-4000-8000-000000000002", 1, "create-key-1", nil)
	expectAPIError(t, err, "IDEMPOTENCY_CONFLICT")
	vehicle, err := client.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "holding" || vehicle.Version != 2 {
		t.Fatalf("holding vehicle: %+v %v", vehicle, err)
	}
	_, err = client.CheckoutCreate(ctx, "8000000000000000002", firstVehicleID, 1, "create-key-2", nil)
	expectAPIError(t, err, "VEHICLE_UNAVAILABLE")
	cancelledResult, err := client.CheckoutCancel(ctx, driverID, checkout.ID, checkout.Version, "cancel-key-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := dataapi.DecodeAggregate[dataapi.Checkout](cancelledResult)
	if err != nil || cancelled.Status != "cancelled" || cancelled.Version != 2 {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	lookup, err := client.OwnCommandResult(ctx, driverID, "cancel-key-1", "checkout.cancel")
	if err != nil || lookup.Operation != "checkout.cancel" {
		t.Fatalf("lookup: %+v %v", lookup, err)
	}
	_, err = client.OwnCommandResult(ctx, "8000000000000000002", "cancel-key-1", "checkout.cancel")
	expectAPIError(t, err, "NOT_FOUND")
	vehicle, err = client.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" || vehicle.Version != 3 {
		t.Fatalf("released vehicle: %+v %v", vehicle, err)
	}
}

func TestHoldExpiresAndConcurrentTakeHasOneWinner(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { mu.Lock(); defer mu.Unlock(); return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	actors := []string{driverID, "8000000000000000002"}
	results := make(chan error, 2)
	for i, actor := range actors {
		go func(i int, actor string) {
			_, err := client.CheckoutCreate(ctx, actor, firstVehicleID, 1, "race-key-"+string(rune('1'+i)), nil)
			results <- err
		}(i, actor)
	}
	var wins int
	for range actors {
		err := <-results
		if err == nil {
			wins++
			continue
		}
		expectAPIError(t, err, "VEHICLE_UNAVAILABLE")
	}
	if wins != 1 {
		t.Fatalf("expected one hold, got %d", wins)
	}
	mu.Lock()
	clock = clock.Add(15*time.Minute + time.Second)
	mu.Unlock()
	vehicle, err := client.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" || vehicle.Version != 3 {
		t.Fatalf("expired vehicle: %+v %v", vehicle, err)
	}
	_, err = client.CheckoutCreate(ctx, "8000000000000000003", firstVehicleID, 3, "after-expiry-key", nil)
	if err != nil {
		t.Fatalf("vehicle unavailable after expiry: %v", err)
	}
}

func TestExpiredHoldRejectsOldCancelAndBlockedDriverCannotTake(t *testing.T) {
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	_, err = client.CheckoutCreate(ctx, "8000000000000000004", firstVehicleID, 1, "blocked-key", nil)
	expectAPIError(t, err, "CANNOT_START_TRIP")
	result, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "expires-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(15*time.Minute + time.Second)
	_, err = client.CheckoutCancel(ctx, driverID, checkout.ID, checkout.Version, "late-cancel-key", nil)
	expectAPIError(t, err, "HOLD_EXPIRED")
	vehicle, err := client.Vehicle(ctx, driverID, firstVehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("hold not released: %+v %v", vehicle, err)
	}
}
