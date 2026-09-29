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

func TestDuringIssueConversationRequiresOwnedActiveTrip(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	tripID := newRequestID()
	trip := dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: mock.employees[driverID].ID, Status: "active", Version: 1}
	mock.trips[tripID] = trip
	me, err := client.Me(ctx, driverID)
	if err != nil || me.Employee == nil {
		t.Fatalf("driver identity: %+v %v", me, err)
	}
	category, description, kind := "mechanical", "Проблема в поездке", "photo"
	version := int64(1)
	input := dataapi.ConversationSaveInput{Flow: "issue_during", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &tripID, TripID: &tripID, VehicleID: &trip.VehicleID, VehicleVersion: &version, IssueCategory: &category, DraftText: &description}}
	if _, err := client.ConversationSave(ctx, "8000000000000000002", me.Employee.ID, 1, input, "during-foreign", nil); err == nil {
		t.Fatal("foreign actor saved trip issue draft")
	} else {
		expectAPIError(t, err, "NOT_FOUND")
	}
	invalid := input
	invalid.Flow = "unsupported_flow"
	if _, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, invalid, "during-unknown-flow", nil); err == nil {
		t.Fatal("unsupported conversation flow accepted")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
	wrongID := newRequestID()
	invalid = input
	invalid.Context.TargetID = &wrongID
	if _, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, invalid, "during-wrong-trip", nil); err == nil {
		t.Fatal("draft accepted with mismatched trip")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
	result, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, input, "during-save", nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != 2 || saved.Flow != "issue_during" {
		t.Fatalf("saved draft: %+v %v", saved, err)
	}
	if _, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 1, input, "during-stale", nil); err == nil {
		t.Fatal("stale draft version accepted")
	} else {
		expectAPIError(t, err, "STALE_VERSION")
	}
	trip.Status = "completed"
	mock.trips[tripID] = trip
	if _, err := client.ConversationSave(ctx, driverID, me.Employee.ID, 2, input, "during-completed", nil); err == nil {
		t.Fatal("completed trip accepted new issue draft")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
}

func TestAfterIssueConversationRequiresCurrentOwnedReturn(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tripID, returnID, inspectionID := newRequestID(), newRequestID(), newRequestID()
	employee := mock.employees[driverID]
	employee.ActiveTripID = &tripID
	mock.employees[driverID] = employee
	mock.vehicles[0].Status = "in_trip"
	mock.trips[tripID] = dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, Status: "returning", ReturnID: &returnID, Version: 2}
	mock.returns[returnID] = dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", Step: "checklist", IntentConfirmedAt: &now, Inspection: dataapi.Inspection{ID: inspectionID, Phase: "after", Status: "draft", Version: 1}, Version: 1}
	client := commandClient(t, mock)
	ctx := context.Background()
	category, description, kind := "cleanliness", "Грязный салон", "photo"
	vehicleVersion := int64(1)
	input := dataapi.ConversationSaveInput{Flow: "issue_after", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &inspectionID, TripID: &tripID, ReturnID: &returnID, VehicleID: &mock.vehicles[0].ID, VehicleVersion: &vehicleVersion, IssueCategory: &category, DraftText: &description}}
	if _, err := client.ConversationSave(ctx, "8000000000000000002", employee.ID, 1, input, "after-foreign", nil); err == nil {
		t.Fatal("foreign actor saved after draft")
	} else {
		expectAPIError(t, err, "NOT_FOUND")
	}
	wrongReturn := newRequestID()
	invalid := input
	invalid.Context.ReturnID = &wrongReturn
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 1, invalid, "after-wrong-return", nil); err == nil {
		t.Fatal("draft accepted for another return")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 1, input, "after-save", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 1, input, "after-stale", nil); err == nil {
		t.Fatal("stale version accepted")
	} else {
		expectAPIError(t, err, "STALE_VERSION")
	}
	draft := mock.returns[returnID]
	draft.Status = "cancelled"
	mock.returns[returnID] = draft
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 2, input, "after-cancelled", nil); err == nil {
		t.Fatal("cancelled return accepted after draft")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
}
