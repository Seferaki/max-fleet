package datamock

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func testBeforeIssueNotification(t *testing.T, mock *Server) string {
	t.Helper()
	client := commandClient(t, mock)
	created, err := client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "notification-hold-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	input := dataapi.IssueCreateInput{Category: "mechanical", Description: "Демо: нужна проверка", InspectionID: &hold.Inspection.ID}
	issued, err := client.IssueCreate(context.Background(), driverID, firstVehicleID, 2, input, "notification-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](issued)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.IssueCreate(context.Background(), driverID, firstVehicleID, 2, input, "notification-issue-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	return issue.ID
}

func TestNotificationOutboxClaimRestartExpiryAndAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	issueID := testBeforeIssueNotification(t, mock)
	if len(mock.notifications) != 1 {
		t.Fatalf("domain retry duplicated outbox: %d", len(mock.notifications))
	}
	claimPath := "/internal/v1/notifications/claim"
	input := map[string]any{"worker_id": "sender-a", "max_items": 10}
	status, _ := integrationRequest(t, mock.Handler(), http.MethodPost, claimPath, "test-service-token", "1.3", "claim-key-001", input)
	if status != http.StatusUnauthorized {
		t.Fatalf("actor service token accepted: %d", status)
	}
	status, _ = integrationRequest(t, mock.Handler(), http.MethodPost, claimPath, "worker-token", "2.0", "claim-key-001", input)
	if status != http.StatusBadRequest {
		t.Fatalf("wrong contract version accepted: %d", status)
	}
	status, body := integrationRequest(t, mock.Handler(), http.MethodPost, claimPath, "worker-token", "1.3", "claim-key-001", input)
	claim := integrationData[dataapi.NotificationClaim](t, body)
	if status != http.StatusOK || len(claim.Items) != 1 || claim.Items[0].Event.Type != "issue_created" || claim.Items[0].Event.ResourceID != issueID || claim.Items[0].RecipientMaxUserID != "8000000000000000003" || claim.Items[0].Attempt != 1 || claim.Items[0].LeaseToken == "" {
		t.Fatalf("notification claim: %d %+v", status, claim)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, claimPath, "worker-token", "1.3", "claim-key-001", input)
	replayed := integrationData[dataapi.NotificationClaim](t, body)
	if status != http.StatusOK || len(replayed.Items) != 1 || replayed.Items[0].LeaseToken != claim.Items[0].LeaseToken {
		t.Fatalf("restart replay: %d %+v", status, replayed)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, claimPath, "worker-token", "1.3", "claim-key-002", map[string]any{"worker_id": "sender-b", "max_items": 10})
	if status != http.StatusOK || len(integrationData[dataapi.NotificationClaim](t, body).Items) != 0 {
		t.Fatalf("second worker claimed active lease: %d", status)
	}
	clock = clock.Add(notificationLeaseDuration + time.Second)
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, claimPath, "worker-token", "1.3", "claim-key-003", map[string]any{"worker_id": "sender-b", "max_items": 10})
	afterExpiry := integrationData[dataapi.NotificationClaim](t, body)
	if status != http.StatusOK || len(afterExpiry.Items) != 1 || afterExpiry.Items[0].Attempt != 2 || afterExpiry.Items[0].LeaseToken == claim.Items[0].LeaseToken {
		t.Fatalf("expired notification lease not fenced: %d %+v", status, afterExpiry)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, claimPath, "worker-token", "1.3", "claim-key-001", input)
	if status != http.StatusConflict || integrationErrorCode(t, body) != "LEASE_EXPIRED" {
		t.Fatalf("expired claim replay accepted: %d", status)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, claimPath, "worker-token", "1.3", "claim-key-003", map[string]any{"worker_id": "sender-c", "max_items": 10})
	if status != http.StatusConflict || integrationErrorCode(t, body) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("changed claim replay accepted: %d", status)
	}
}

func TestNotificationClaimRollbackAndV12Upgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	previous := mock.snapshot()
	previous.Version = 12
	previous.Notifications = nil
	previous.NotificationClaims = nil
	previous.NotificationSequence = 0
	if err := atomicSave(path, previous); err != nil {
		t.Fatal(err)
	}
	mock, err = NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil || mock.notifications == nil {
		t.Fatalf("v12 upgrade: %v", err)
	}
	testBeforeIssueNotification(t, mock)
	save := mock.saveSnapshot
	mock.saveSnapshot = func(stateSnapshot) error { return errors.New("synthetic disk failure") }
	status, body := integrationRequest(t, mock.Handler(), http.MethodPost, "/internal/v1/notifications/claim", "worker-token", "1.3", "claim-key-001", map[string]any{"worker_id": "sender-a", "max_items": 1})
	if status != http.StatusServiceUnavailable || integrationErrorCode(t, body) != "DATABASE_UNAVAILABLE" || len(mock.notificationClaims) != 0 {
		t.Fatalf("failed claim acknowledged: %d", status)
	}
	for _, notification := range mock.notifications {
		if notification.Status != "pending" || notification.Attempt != 0 || notification.LeaseToken != "" {
			t.Fatalf("claim survived failed save: %+v", notification)
		}
	}
	mock.saveSnapshot = save
	status, body = integrationRequest(t, mock.Handler(), http.MethodPost, "/internal/v1/notifications/claim", "worker-token", "1.3", "claim-key-001", map[string]any{"worker_id": "sender-a", "max_items": 1})
	if status != http.StatusOK || len(integrationData[dataapi.NotificationClaim](t, body).Items) != 1 {
		t.Fatalf("claim after storage recovery: %d", status)
	}
}
