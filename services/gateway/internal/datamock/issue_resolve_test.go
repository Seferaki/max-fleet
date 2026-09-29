package datamock

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestIssueResolveAssignsVerifiedAdminAndRecovers(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC)
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := NewWithSnapshot("test-service-token", snapshotPath, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	issueID := "40000000-0000-4000-8000-000000000201"
	driver := mock.employees[driverID]
	mock.issues[issueID] = dataapi.Issue{
		ID: issueID, VehicleID: firstVehicleID, AuthorID: driver.ID, Stage: "during", Category: "mechanical",
		Description: "Синтетическая неисправность", Status: "open", BlocksIssuance: true,
		TripID: nil, InspectionID: nil, AssetIDs: []string{}, Version: 1, UpdatedAt: clock,
	}
	firstAdmin := mock.employees[adminActorID]
	secondAdmin := firstAdmin
	secondAdmin.ID = "80000000-0000-4000-8000-000000000206"
	secondAdmin.MaxUserID = secondAdminID
	mock.employees[secondAdminID] = secondAdmin
	client := commandClient(t, mock)

	input := dataapi.IssueResolveInput{Status: "in_progress", Comment: "Проверяю причину сбоя", Confirmation: true}
	status, _, code := postRawCommand(t, mock, adminActorID, "issue-assignment-forged", "issue.resolve", issueID, 1,
		map[string]any{"status": "in_progress", "comment": input.Comment, "confirmation": true, "assigned_to": secondAdmin.ID})
	if status != 400 || code != "INVALID_REQUEST" || mock.issues[issueID].AssignedTo != nil || len(mock.issueActions) != 0 {
		t.Fatalf("caller-selected assignee was accepted: status=%d code=%s issue=%+v", status, code, mock.issues[issueID])
	}
	_, err = client.IssueResolve(ctx, driverID, issueID, 1, input, "issue-assignment-nonadmin", nil)
	expectAPIError(t, err, "ADMIN_REQUIRED")
	_, err = client.IssueResolve(ctx, adminActorID, issueID, 2, input, "issue-assignment-stale", nil)
	expectAPIError(t, err, "STALE_VERSION")
	if mock.issues[issueID].AssignedTo != nil || len(mock.issueActions) != 0 {
		t.Fatal("forbidden or stale issue command changed state")
	}
	saveSnapshot := mock.saveSnapshot
	mock.saveSnapshot = func(stateSnapshot) error { return errors.New("injected issue audit save failure") }
	_, err = client.IssueResolve(ctx, adminActorID, issueID, 1, input, "issue-assignment-save-failure", nil)
	expectAPIError(t, err, "TEMPORARY_FAILURE")
	mock.saveSnapshot = saveSnapshot
	if mock.issues[issueID].Status != "open" || mock.issues[issueID].AssignedTo != nil || len(mock.issueActions) != 0 {
		t.Fatal("failed snapshot save left issue assignment or audit in memory")
	}

	started, err := client.IssueResolve(ctx, adminActorID, issueID, 1, input, "issue-assignment-start", nil)
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := dataapi.DecodeAggregate[dataapi.Issue](started)
	if err != nil || assigned.Status != "in_progress" || assigned.AssignedTo == nil || *assigned.AssignedTo != firstAdmin.ID ||
		!assigned.BlocksIssuance || assigned.Version != 2 || assigned.ResolutionComment != nil || assigned.ResolvedBy != nil || assigned.ResolvedAt != nil {
		t.Fatalf("in-progress issue did not record its admin assignee: %+v %v", assigned, err)
	}
	if len(mock.notifications) != 0 {
		t.Fatal("taking an issue in progress emitted a terminal-resolution notification")
	}
	vehicleIndex := mock.vehicleIndex(firstVehicleID)
	mock.vehicles[vehicleIndex].ManualBlocked = true
	mock.vehicles[vehicleIndex].Status = "unavailable"
	unblockReason := "Проверка замечания завершена"
	reviewCompleted := true
	vehicleVersion := mock.vehicles[vehicleIndex].Version
	proofID := solvedAdminProof(t, client, "vehicle_unblock", dataapi.AdminChallengeIntent{
		Operation: "vehicle.unblock", TargetID: stringPointer(firstVehicleID), ExpectedVersion: &vehicleVersion,
		Reason: &unblockReason, ReviewCompleted: &reviewCompleted,
	}, "issue-blocking-unblock")
	_, err = client.VehicleUnblock(ctx, adminActorID, firstVehicleID, vehicleVersion, unblockReason, proofID, true, "issue-blocking-unblock", nil)
	expectAPIError(t, err, "INVALID_STATE")
	if mock.challenges[proofID].ProofConsumed {
		t.Fatal("blocking issue consumed the vehicle-unblock proof")
	}
	replayed, err := client.IssueResolve(ctx, adminActorID, issueID, 1, input, "issue-assignment-start", nil)
	if err != nil || !reflect.DeepEqual(replayed, started) || len(mock.issueActions) != 1 {
		t.Fatalf("same-key retry changed issue/audit: %+v %v audit=%d", replayed, err, len(mock.issueActions))
	}
	changed := input
	changed.Comment = "Другой текст"
	_, err = client.IssueResolve(ctx, adminActorID, issueID, 1, changed, "issue-assignment-start", nil)
	expectAPIError(t, err, "IDEMPOTENCY_CONFLICT")

	closed, err := client.IssueResolve(ctx, secondAdminID, issueID, assigned.Version,
		dataapi.IssueResolveInput{Status: "resolved", Comment: "Проверено, исправлено", Confirmation: true},
		"issue-assignment-resolve", nil)
	if err != nil {
		t.Fatal(err)
	}
	unblockedResult, err := client.VehicleUnblock(ctx, adminActorID, firstVehicleID, vehicleVersion, unblockReason, proofID, true, "issue-blocking-unblock", nil)
	if err != nil {
		t.Fatalf("resolved issue did not release vehicle-unblock gate: %v", err)
	}
	unblocked, err := dataapi.DecodeAggregate[dataapi.Vehicle](unblockedResult)
	if err != nil || unblocked.ManualBlocked || unblocked.Status != "available" {
		t.Fatalf("vehicle after issue resolution: %+v %v", unblocked, err)
	}
	resolved, err := dataapi.DecodeAggregate[dataapi.Issue](closed)
	if err != nil || resolved.Status != "resolved" || resolved.BlocksIssuance || resolved.AssignedTo == nil ||
		*resolved.AssignedTo != firstAdmin.ID || resolved.ResolvedBy == nil || *resolved.ResolvedBy != secondAdmin.ID ||
		resolved.ResolutionComment == nil || *resolved.ResolutionComment != "Проверено, исправлено" || resolved.ResolvedAt == nil ||
		!resolved.ResolvedAt.Equal(clock) || resolved.Version != 3 {
		t.Fatalf("terminal transition lost assignee or resolver: %+v %v", resolved, err)
	}
	if len(mock.issueActions) != 2 || mock.issueActions[0].ActorEmployeeID != firstAdmin.ID ||
		mock.issueActions[0].PreviousStatus != "open" || mock.issueActions[0].NextStatus != "in_progress" ||
		mock.issueActions[0].Comment != input.Comment || mock.issueActions[1].ActorEmployeeID != secondAdmin.ID {
		t.Fatalf("issue transition audit missing actor/comment: %+v", mock.issueActions)
	}
	if len(mock.notifications) != 2 {
		t.Fatalf("terminal issue resolution did not notify both admins exactly once: %d", len(mock.notifications))
	}

	restarted, err := NewWithSnapshot("test-service-token", snapshotPath, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	restartedClient := commandClient(t, restarted)
	restored, err := restartedClient.Issue(ctx, driverID, issueID)
	if err != nil || restored.AssignedTo == nil || *restored.AssignedTo != firstAdmin.ID || restored.ResolvedBy == nil ||
		*restored.ResolvedBy != secondAdmin.ID || restored.ResolutionComment == nil || len(restarted.issueActions) != 2 {
		t.Fatalf("issue resolution did not recover from snapshot: %+v %v audit=%+v", restored, err, restarted.issueActions)
	}
	replayedAfterRestart, err := restartedClient.IssueResolve(ctx, adminActorID, issueID, 1, input, "issue-assignment-start", nil)
	replayedIssue, decodeErr := dataapi.DecodeAggregate[dataapi.Issue](replayedAfterRestart)
	if err != nil || decodeErr != nil || replayedIssue.Status != "in_progress" || replayedIssue.AssignedTo == nil ||
		*replayedIssue.AssignedTo != firstAdmin.ID || len(restarted.issueActions) != 2 || len(restarted.notifications) != 2 {
		t.Fatalf("same-key issue replay after restart did not recover original result: %+v %v %v", replayedIssue, err, decodeErr)
	}
}

func TestIssueSnapshotMigrationAddsProjectionFieldsToCachedResult(t *testing.T) {
	clock := time.Date(2026, 9, 29, 17, 5, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	issueID := "40000000-0000-4000-8000-000000000202"
	legacyAggregate := json.RawMessage(`{"id":"40000000-0000-4000-8000-000000000202","vehicle_id":"10000000-0000-4000-8000-000000000001","author_id":"80000000-0000-4000-8000-000000000001","stage":"during","category":"mechanical","description":"Legacy issue","status":"open","blocks_issuance":true,"trip_id":null,"inspection_id":null,"asset_ids":[],"version":1,"updated_at":"2026-09-29T17:05:00Z"}`)
	state := mock.snapshot()
	state.Version = 17
	state.Commands[driverID+":legacy-issue-key"] = commandRecord{
		Signature: "legacy-signature", Result: dataapi.CommandResult{Operation: "issue.create", Aggregate: legacyAggregate},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var oldSnapshot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &oldSnapshot); err != nil {
		t.Fatal(err)
	}
	delete(oldSnapshot, "issue_actions")
	raw, err = json.Marshal(oldSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	upgraded, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	result := upgraded.commands[driverID+":legacy-issue-key"].Result
	var projection map[string]any
	if err := json.Unmarshal(result.Aggregate, &projection); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"assigned_to", "resolution_comment", "resolved_by", "resolved_at"} {
		if value, exists := projection[field]; !exists || value != nil {
			t.Fatalf("legacy cached issue omitted nullable %s: %#v", field, projection)
		}
	}
	if upgraded.issueActions == nil || len(upgraded.issueActions) != 0 {
		t.Fatalf("legacy snapshot did not initialize issue audit: %#v", upgraded.issueActions)
	}
	if err := upgraded.persist(); err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if raw, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if string(saved["version"]) != "18" {
		t.Fatalf("snapshot was not upgraded: version=%s issue=%s", saved["version"], issueID)
	}
}
