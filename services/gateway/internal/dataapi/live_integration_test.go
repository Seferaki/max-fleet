package dataapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in smoke test exercises the production Go client against a live Python Data API.
// It uses only the synthetic seed and cancels the hold it creates.
func TestLivePythonDataAPI(t *testing.T) {
	if os.Getenv("MAX_FLEET_LIVE_DATA_API") != "1" {
		t.Skip("set MAX_FLEET_LIVE_DATA_API=1 to run against a disposable synthetic Data API")
	}

	baseURL := os.Getenv("MAX_FLEET_LIVE_DATA_API_URL")
	if baseURL == "" {
		t.Fatal("MAX_FLEET_LIVE_DATA_API_URL is required")
	}
	actor := os.Getenv("MAX_FLEET_LIVE_DATA_API_ACTOR")
	if actor == "" {
		actor = "8000000000000000001"
	}
	tokenPath := os.Getenv("MAX_FLEET_LIVE_DATA_API_TOKEN_FILE")
	if tokenPath == "" {
		t.Fatal("MAX_FLEET_LIVE_DATA_API_TOKEN_FILE is required")
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read Data API token file: %v", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		t.Fatal("Data API token file is empty")
	}

	client, err := New(Config{BaseURL: baseURL, Token: token, HTTPClient: &http.Client{Timeout: 10 * time.Second}})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	meta, err := client.Meta(ctx)
	if err != nil {
		t.Fatalf("GET /meta: %v", err)
	}
	if meta.ContractVersion != ContractVersion || meta.Mode != "real" {
		t.Fatalf("unexpected Python Data API metadata: version=%q mode=%q", meta.ContractVersion, meta.Mode)
	}
	me, err := client.Me(ctx, actor)
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	if !me.Allowed || me.Employee == nil || me.Employee.Role != "employee" {
		t.Fatalf("synthetic actor is not an allowed employee: allowed=%t", me.Allowed)
	}
	if _, err := client.AdminSummary(ctx, actor); err == nil {
		t.Fatal("employee unexpectedly received admin summary")
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != "ACCESS_DENIED" {
			t.Fatalf("unexpected admin ACL result: %v", err)
		}
	}
	if _, err := client.CurrentRules(ctx, actor); err != nil {
		t.Fatalf("GET /rules/current: %v", err)
	}
	if _, err := client.State(ctx, actor); err != nil {
		t.Fatalf("GET /state: %v", err)
	}

	available := true
	vehicles, err := client.Vehicles(ctx, actor, VehicleFilter{Available: &available, Limit: 10})
	if err != nil {
		t.Fatalf("GET /vehicles: %v", err)
	}
	if len(vehicles.Items) == 0 {
		t.Fatal("synthetic seed has no available vehicle")
	}
	vehicle := vehicles.Items[0]
	createKey := liveIdempotencyKey(t)
	cancelKey := liveIdempotencyKey(t)
	created, err := client.CheckoutCreate(ctx, actor, vehicle.ID, vehicle.Version, createKey, nil)
	if err != nil {
		t.Fatalf("checkout.create: %v", err)
	}
	checkout, err := DecodeAggregate[Checkout](created)
	if err != nil {
		t.Fatalf("decode checkout.create aggregate: %v", err)
	}
	if checkout.Status != "holding" || checkout.VehicleID != vehicle.ID {
		t.Fatalf("unexpected checkout aggregate: status=%q vehicle_matches=%t", checkout.Status, checkout.VehicleID == vehicle.ID)
	}
	// Always cancel, including if a retry or later assertion fails.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, cleanupErr := client.CheckoutCancel(cleanupCtx, actor, checkout.ID, checkout.Version, cancelKey, nil)
		if cleanupErr != nil {
			t.Errorf("cancel synthetic checkout: %v", cleanupErr)
		}
	})
	// Retry with the same key must recover the original command result, not create another hold.
	replayed, err := client.CheckoutCreate(ctx, actor, vehicle.ID, vehicle.Version, createKey, nil)
	if err != nil {
		t.Fatalf("idempotent checkout.create retry: %v", err)
	}
	recovered, err := DecodeAggregate[Checkout](replayed)
	if err != nil || recovered.ID != checkout.ID {
		t.Fatalf("checkout idempotency did not recover the original hold: same_id=%t decode_error=%v", recovered.ID == checkout.ID, err)
	}

	state, err := client.State(ctx, actor)
	if err != nil {
		t.Fatalf("GET /state with active hold: %v", err)
	}
	if state.Checkout == nil || state.Checkout.ID != checkout.ID {
		t.Fatal("/state did not return the checkout created by this client")
	}
	if _, err := client.CheckoutCancel(ctx, actor, checkout.ID, checkout.Version, cancelKey, nil); err != nil {
		t.Fatalf("checkout.cancel: %v", err)
	}
}

func liveIdempotencyKey(t *testing.T) string {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatalf("generate live smoke key: %v", err)
	}
	return "live-" + hex.EncodeToString(bytes[:])
}
