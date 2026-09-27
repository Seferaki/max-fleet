package datamock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestStageIssueAssetOwnershipRetryRestartAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "stage-hold-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	input := dataapi.IssueStageInput{ScopeType: "inspection", ScopeID: checkout.Inspection.ID, SourceEventKey: "issue-event-one", IdempotencyKey: "issue-stage-one", ContentType: "image/png", Image: syntheticPNG(t, 42)}
	_, err = client.StageIssueAsset(ctx, "8000000000000000002", input)
	expectAPIError(t, err, "NOT_FOUND")
	first, err := client.StageIssueAsset(ctx, driverID, input)
	if err != nil || first.ExpiresAt.Sub(now) != 30*time.Minute {
		t.Fatalf("stage: %+v %v", first, err)
	}
	if len(mock.issueAssets) != 1 || len(mock.photos[checkout.Inspection.ID]) != 0 {
		t.Fatal("staged issue image changed inspection photo slots")
	}
	replay, err := client.StageIssueAsset(ctx, driverID, input)
	if err != nil || replay.AssetID != first.AssetID {
		t.Fatalf("retry: %+v %v", replay, err)
	}
	changed := input
	changed.SourceEventKey = "changed-event"
	_, err = client.StageIssueAsset(ctx, driverID, changed)
	expectAPIError(t, err, "IDEMPOTENCY_CONFLICT")
	changed.IdempotencyKey = "different-key-1"
	_, err = client.StageIssueAsset(ctx, driverID, changed)
	expectAPIError(t, err, "DUPLICATE_PHOTO")
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	replay, err = commandClient(t, restarted).StageIssueAsset(ctx, driverID, input)
	if err != nil || replay.AssetID != first.AssetID {
		t.Fatalf("restart retry: %+v %v", replay, err)
	}
	second := input
	second.Image = syntheticPNG(t, 43)
	second.SourceEventKey = "issue-event-two"
	second.IdempotencyKey = "issue-stage-two"
	restarted.saveSnapshot = func(stateSnapshot) error { return os.ErrPermission }
	_, err = commandClient(t, restarted).StageIssueAsset(ctx, driverID, second)
	expectAPIError(t, err, "STORAGE_UNAVAILABLE")
	if len(restarted.issueAssets) != 1 {
		t.Fatal("failed save retained staged asset")
	}
	restarted.saveSnapshot = func(state stateSnapshot) error { return atomicSave(path, state) }
	staged, err := commandClient(t, restarted).StageIssueAsset(ctx, driverID, second)
	if err != nil || staged.AssetID == first.AssetID {
		t.Fatalf("retry after failed save: %+v %v", staged, err)
	}
	if err := os.Remove(filepath.Join(restarted.assetDir, first.AssetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now }); err == nil {
		t.Fatal("damaged staged asset accepted on restart")
	}
}
