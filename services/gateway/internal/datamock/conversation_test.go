package datamock

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestIssueConversationCASOwnerAndRestart(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "draft-setup-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	// The test fixture begins after the driver's new-damage answer.
	damage := true
	hold.Inspection.NewDamage = &damage
	mock.checkouts[hold.ID] = hold
	if err := mock.persist(); err != nil {
		t.Fatal(err)
	}
	staged, err := client.StageIssueAsset(ctx, driverID, dataapi.IssueStageInput{ScopeType: "inspection", ScopeID: hold.Inspection.ID, SourceEventKey: "draft-photo-1", IdempotencyKey: "draft-stage-1", ContentType: "image/png", Image: syntheticPNG(t, 11)})
	if err != nil {
		t.Fatal(err)
	}
	me, err := client.Me(ctx, driverID)
	if err != nil || me.Employee == nil {
		t.Fatalf("driver identity: %+v %v", me, err)
	}
	category, description, kind := "body_damage", "Царапина на левом крыле", "photo"
	vehicleVersion := int64(2)
	input := dataapi.ConversationSaveInput{Flow: "issue_before", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &hold.Inspection.ID, VehicleID: &hold.VehicleID, VehicleVersion: &vehicleVersion, IssueCategory: &category, DraftText: &description, AssetIDs: []string{staged.AssetID}}}
	if _, err := client.ConversationSave(ctx, "8000000000000000002", me.Employee.ID, 1, input, "draft-foreign", nil); err == nil {
		t.Fatal("other actor saved the driver's conversation")
	} else {
		expectAPIError(t, err, "NOT_FOUND")
	}
	result, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, input, "draft-save-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != 2 || len(saved.Context.AssetIDs) != 1 || saved.Context.AssetIDs[0] != staged.AssetID {
		t.Fatalf("saved conversation: %+v %v", saved, err)
	}
	if replay, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, input, "draft-save-1", nil); err != nil {
		t.Fatalf("same command retry: %v", err)
	} else if same, err := dataapi.DecodeAggregate[dataapi.Conversation](replay); err != nil || same.Version != 2 {
		t.Fatalf("duplicate mutated version: %+v %v", same, err)
	}
	if _, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, input, "draft-stale-1", nil); err == nil {
		t.Fatal("stale conversation version accepted")
	} else {
		expectAPIError(t, err, "STALE_VERSION")
	}
	otherState, err := client.State(ctx, "8000000000000000002")
	if err != nil || otherState.Conversation != nil || otherState.ConversationVersion != 1 {
		t.Fatalf("other actor sees draft: %+v %v", otherState, err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	state, err := commandClient(t, restarted).State(ctx, driverID)
	if err != nil || state.Conversation == nil || state.ConversationVersion != 2 || state.Conversation.Context.DraftText == nil || *state.Conversation.Context.DraftText != description || len(state.Conversation.Context.AssetIDs) != 1 || state.Conversation.Context.AssetIDs[0] != staged.AssetID {
		t.Fatalf("restart lost issue draft: %+v %v", state, err)
	}
}
