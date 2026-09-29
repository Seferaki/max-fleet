package datamock

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func claimOneNotification(t *testing.T, mock *Server, key string) dataapi.NotificationLease {
	t.Helper()
	status, body := integrationRequest(t, mock.Handler(), http.MethodPost, "/internal/v1/notifications/claim", "worker-token", "1.13", key, map[string]any{"worker_id": "sender-a", "max_items": 1})
	claim := integrationData[dataapi.NotificationClaim](t, body)
	if status != http.StatusOK || len(claim.Items) != 1 {
		t.Fatalf("notification claim: %d %+v", status, claim)
	}
	return claim.Items[0]
}

func TestNotificationAckLeaseIdempotencyAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	testBeforeIssueNotification(t, mock)
	lease := claimOneNotification(t, mock, "notice-claim-1")
	pathAck := "/internal/v1/notifications/" + lease.DeliveryID + "/ack"
	input := map[string]any{"lease_token": lease.LeaseToken, "provider_message_id": "synthetic-message-1"}
	if status, _ := integrationRequest(t, mock.Handler(), http.MethodPost, pathAck, "test-service-token", "1.13", "notice-ack-1", input); status != http.StatusUnauthorized {
		t.Fatalf("data bearer accepted on worker route: %d", status)
	}
	if status, body := integrationRequest(t, mock.Handler(), http.MethodPost, pathAck, "worker-token", "1.13", "notice-ack-1", map[string]any{"lease_token": "wrong-token", "provider_message_id": "synthetic-message-1"}); status != http.StatusConflict || integrationErrorCode(t, body) != "LEASE_EXPIRED" {
		t.Fatalf("wrong token accepted: %d", status)
	}
	status, body := integrationRequest(t, mock.Handler(), http.MethodPost, pathAck, "worker-token", "1.13", "notice-ack-1", input)
	result := integrationData[dataapi.QueueTransition](t, body)
	if status != http.StatusOK || result.State != "sent" || result.ID != lease.DeliveryID || mock.notifications[lease.DeliveryID].ProviderID == nil {
		t.Fatalf("ack failed: %d %+v", status, result)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, pathAck, "worker-token", "1.13", "notice-ack-1", input)
	replay := integrationData[dataapi.QueueTransition](t, body)
	if status != http.StatusOK || replay != result {
		t.Fatalf("ack replay after restart: %d %+v", status, replay)
	}
	if status, body := integrationRequest(t, restarted.Handler(), http.MethodPost, pathAck, "worker-token", "1.13", "notice-ack-1", map[string]any{"lease_token": lease.LeaseToken, "provider_message_id": "changed"}); status != http.StatusConflict || integrationErrorCode(t, body) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("changed ack replay accepted: %d", status)
	}
	if status, body := integrationRequest(t, restarted.Handler(), http.MethodPost, pathAck, "worker-token", "1.13", "notice-ack-2", input); status != http.StatusConflict || integrationErrorCode(t, body) != "LEASE_EXPIRED" {
		t.Fatalf("second ack accepted: %d", status)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, "/internal/v1/notifications/claim", "worker-token", "1.13", "notice-claim-2", map[string]any{"worker_id": "sender-b", "max_items": 1})
	if status != http.StatusOK || len(integrationData[dataapi.NotificationClaim](t, body).Items) != 0 {
		t.Fatalf("sent notification reclaimed: %d", status)
	}
}

func TestNotificationRetryBackoffDeadAndFailedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	testBeforeIssueNotification(t, mock)
	for attempt := 1; attempt <= maxNotificationAttempts; attempt++ {
		lease := claimOneNotification(t, mock, "notice-claim-"+string(rune('a'+attempt)))
		if lease.Attempt != attempt {
			t.Fatalf("attempt %d became %d", attempt, lease.Attempt)
		}
		retryPath := "/internal/v1/notifications/" + lease.DeliveryID + "/retry"
		retryAt := clock.Add(30 * time.Second)
		input := map[string]any{"lease_token": lease.LeaseToken, "error_code": "MAX_RATE_LIMIT", "retry_after": retryAt, "dead": false}
		if attempt == 1 {
			save := mock.saveSnapshot
			mock.saveSnapshot = func(stateSnapshot) error { return errors.New("synthetic disk failure") }
			status, body := integrationRequest(t, mock.Handler(), http.MethodPost, retryPath, "worker-token", "1.13", "notice-retry-1", input)
			if status != http.StatusServiceUnavailable || integrationErrorCode(t, body) != "DATABASE_UNAVAILABLE" || mock.notifications[lease.DeliveryID].Status != "leased" {
				t.Fatalf("failed retry acknowledged: %d", status)
			}
			mock.saveSnapshot = save
		}
		key := "notice-retry-" + string(rune('a'+attempt))
		status, body := integrationRequest(t, mock.Handler(), http.MethodPost, retryPath, "worker-token", "1.13", key, input)
		result := integrationData[dataapi.QueueTransition](t, body)
		expected := "retry"
		if attempt == maxNotificationAttempts {
			expected = "dead"
		}
		if status != http.StatusOK || result.State != expected {
			t.Fatalf("retry %d: %d %+v", attempt, status, result)
		}
		if status, body := integrationRequest(t, mock.Handler(), http.MethodPost, retryPath, "worker-token", "1.13", key, input); status != http.StatusOK || integrationData[dataapi.QueueTransition](t, body) != result {
			t.Fatalf("retry replay %d: %d", attempt, status)
		}
		if status, body := integrationRequest(t, mock.Handler(), http.MethodPost, retryPath, "worker-token", "1.13", key+"-changed", input); status != http.StatusConflict || integrationErrorCode(t, body) != "LEASE_EXPIRED" {
			t.Fatalf("stale retry %d accepted: %d", attempt, status)
		}
		if attempt < maxNotificationAttempts {
			status, body = integrationRequest(t, mock.Handler(), http.MethodPost, "/internal/v1/notifications/claim", "worker-token", "1.13", "notice-wait-"+string(rune('a'+attempt)), map[string]any{"worker_id": "sender-b", "max_items": 1})
			if status != http.StatusOK || len(integrationData[dataapi.NotificationClaim](t, body).Items) != 0 {
				t.Fatalf("notification reclaimed before backoff %d: %d", attempt, status)
			}
		}
		clock = clock.Add(31 * time.Second)
	}
	status, body := integrationRequest(t, mock.Handler(), http.MethodPost, "/internal/v1/notifications/claim", "worker-token", "1.13", "notice-claim-final", map[string]any{"worker_id": "sender-b", "max_items": 1})
	if status != http.StatusOK || len(integrationData[dataapi.NotificationClaim](t, body).Items) != 0 {
		t.Fatalf("dead notification reclaimed: %d", status)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil || len(restarted.notifications) != 1 {
		t.Fatalf("retry state not restored: %v", err)
	}
	for _, item := range restarted.notifications {
		if item.Status != "dead" || item.Attempt != maxNotificationAttempts {
			t.Fatalf("dead state lost: %+v", item)
		}
	}
}

func TestNotificationV13SnapshotUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	testBeforeIssueNotification(t, mock)
	previous := mock.snapshot()
	previous.Version = 13
	previous.NotificationTransitions = nil
	if err := atomicSave(path, previous); err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil || upgraded.notificationTransitions == nil {
		t.Fatalf("v13 snapshot upgrade failed: %v", err)
	}
	lease := claimOneNotification(t, upgraded, "upgrade-claim-1")
	status, body := integrationRequest(t, upgraded.Handler(), http.MethodPost, "/internal/v1/notifications/"+lease.DeliveryID+"/ack", "worker-token", "1.13", "upgrade-ack-1", map[string]any{"lease_token": lease.LeaseToken, "provider_message_id": "synthetic-message-2"})
	if status != http.StatusOK || integrationData[dataapi.QueueTransition](t, body).State != "sent" {
		t.Fatalf("ack after upgrade failed: %d", status)
	}
	if _, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now); err != nil {
		t.Fatalf("v14 snapshot reload failed: %v", err)
	}
}
