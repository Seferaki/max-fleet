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

func TestAdminIssueResolutionConversationRequiresCurrentAdminAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	issueID := "70000000-0000-4000-8000-000000000021"
	mock.issues[issueID] = dataapi.Issue{
		ID: issueID, VehicleID: firstVehicleID, AuthorID: mock.employees[driverID].ID,
		Stage: "during", Category: "mechanical", Description: "Синтетическая проверка",
		Status: "open", BlocksIssuance: true, AssetIDs: []string{}, Version: 1, UpdatedAt: now,
	}
	admin := mock.employees[adminActorID]
	issueVersion, pending := int64(1), "text"
	input := dataapi.ConversationSaveInput{
		Flow: "issue_admin_resolution", Step: "await_comment_resolved", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &issueVersion},
	}
	if _, err := client.ConversationSave(ctx, adminActorID, admin.ID, 1, input, "admin-issue-conv-start", nil); err != nil {
		t.Fatalf("admin could not save comment step: %v", err)
	}
	state, err := client.State(ctx, adminActorID)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "await_comment_resolved" || state.Conversation.Context.IssueVersion == nil || *state.Conversation.Context.IssueVersion != 1 {
		t.Fatalf("admin conversation did not persist: %+v %v", state, err)
	}

	driver := mock.employees[driverID]
	if _, err := client.ConversationSave(ctx, driverID, driver.ID, 1, input, "driver-issue-conv-start", nil); err == nil {
		t.Fatal("employee saved an admin issue-resolution flow")
	}
	staleVersion := int64(2)
	stale := input
	stale.Context.IssueVersion = &staleVersion
	if _, err := client.ConversationSave(ctx, adminActorID, admin.ID, 2, stale, "admin-issue-conv-stale", nil); err == nil {
		t.Fatal("conversation accepted a stale issue version")
	}
	comment, none := "Крепление восстановлено", "none"
	confirmed := dataapi.ConversationSaveInput{
		Flow: "issue_admin_resolution", Step: "confirm_resolved", PendingInputKind: &none,
		Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &issueVersion, DraftText: &comment},
	}
	if _, err := client.ConversationSave(ctx, adminActorID, admin.ID, 2, confirmed, "admin-issue-conv-comment", nil); err != nil {
		t.Fatalf("admin could not persist comment before confirmation: %v", err)
	}

	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := commandClient(t, restarted).State(ctx, adminActorID)
	if err != nil || recovered.Conversation == nil || recovered.Conversation.Step != "confirm_resolved" ||
		recovered.Conversation.Context.DraftText == nil || *recovered.Conversation.Context.DraftText != comment ||
		recovered.Conversation.Context.IssueVersion == nil || *recovered.Conversation.Context.IssueVersion != 1 {
		t.Fatalf("restart lost admin resolution confirmation state: %+v %v", recovered, err)
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
		expectAPIError(t, err, "INVALID_REQUEST")
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

func TestReturnGeoConversationRequiresConfirmedOwnedLocation(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tripID, returnID := newRequestID(), newRequestID()
	employee := mock.employees[driverID]
	employee.ActiveTripID = &tripID
	mock.employees[driverID] = employee
	mock.vehicles[0].Status = "in_trip"
	mock.trips[tripID] = dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, Status: "returning", ReturnID: &returnID, Version: 2}
	mock.returns[returnID] = dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", Step: "checklist", IntentConfirmedAt: &now, Version: 1}
	client := commandClient(t, mock)
	ctx := context.Background()
	coordinates, kind := "55.750000,37.620000", "none"
	input := dataapi.ConversationSaveInput{Flow: "return_location", Step: "confirm", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &returnID, ReturnID: &returnID, TripID: &tripID, VehicleID: &mock.vehicles[0].ID, DraftText: &coordinates}}
	if _, err := client.ConversationSave(ctx, "8000000000000000002", employee.ID, 1, input, "geo-foreign", nil); err == nil {
		t.Fatal("foreign actor saved location preview")
	} else {
		expectAPIError(t, err, "NOT_FOUND")
	}
	bad := input
	invalidCoordinates := "NaN,37.62"
	bad.Context.DraftText = &invalidCoordinates
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 1, bad, "geo-invalid", nil); err == nil {
		t.Fatal("invalid geo preview accepted")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 1, input, "geo-preview", nil); err != nil {
		t.Fatal(err)
	}
	done := input
	done.Step = "done"
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 2, done, "geo-unconfirmed-done", nil); err == nil {
		t.Fatal("conversation marked done without domain location")
	} else {
		expectAPIError(t, err, "INVALID_STATE")
	}
	if _, err := client.ReturnSetLocation(ctx, driverID, returnID, 1, "geo-set-confirmed", nil, dataapi.LocationInput{Latitude: 55.75, Longitude: 37.62, Source: "max_geo", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConversationSave(ctx, driverID, employee.ID, 2, done, "geo-done", nil); err != nil {
		t.Fatal(err)
	}
	state, err := client.State(ctx, driverID)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Return == nil || state.Return.ParkingLocation == nil || state.Return.ParkingLocation.Source != "max_geo" {
		t.Fatalf("confirmed location state: %+v %v", state, err)
	}
}
