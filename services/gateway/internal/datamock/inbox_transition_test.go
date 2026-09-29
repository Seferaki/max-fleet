package datamock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func transitionRequest(t *testing.T, handler http.Handler, id, action, key, token string, next *time.Time) (int, []byte) {
	t.Helper()
	input := map[string]any{"lease_token": token}
	if action == "retry" {
		input["error_code"] = "TEMPORARY_FAILURE"
		input["next_attempt_at"] = next
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/inbox/"+id+"/"+action, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer worker-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("X-Contract-Version", "1.5")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	response := w.Result()
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result
}

func queueTransition(t *testing.T, body []byte) dataapi.QueueTransition {
	t.Helper()
	var envelope struct {
		Data dataapi.QueueTransition `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || !validUUID(envelope.Data.ID) {
		t.Fatalf("invalid transition response: %s %v", body, err)
	}
	return envelope.Data
}

func TestInboxAckFencesAndRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	server, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	status, body := sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "store-key-001", "1.5")
	if status != http.StatusOK {
		t.Fatalf("store: %d %s", status, body)
	}
	status, body = claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 1)
	lease := claimedInbox(t, body).Items[0]
	status, _ = transitionRequest(t, server.Handler(), lease.ID, "ack", "ack-key-001", "wrong-token", nil)
	if status != http.StatusConflict {
		t.Fatalf("wrong lease acknowledged: %d", status)
	}
	status, body = transitionRequest(t, server.Handler(), lease.ID, "ack", "ack-key-001", lease.LeaseToken, nil)
	transition := queueTransition(t, body)
	if status != http.StatusOK || transition.State != "done" || transition.ID != lease.ID {
		t.Fatalf("ack: %d %+v", status, transition)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	status, body = transitionRequest(t, restarted.Handler(), lease.ID, "ack", "ack-key-001", lease.LeaseToken, nil)
	if status != http.StatusOK || queueTransition(t, body).State != "done" {
		t.Fatalf("ack retry after restart: %d %s", status, body)
	}
	status, _ = transitionRequest(t, restarted.Handler(), lease.ID, "ack", "ack-key-002", lease.LeaseToken, nil)
	if status != http.StatusConflict {
		t.Fatalf("old lease reused after ack: %d", status)
	}
	status, body = claimInboxRequest(t, restarted.Handler(), "claim-key-002", "worker-b", 1)
	if status != http.StatusOK || len(claimedInbox(t, body).Items) != 0 {
		t.Fatalf("done event reclaimed: %d %s", status, body)
	}
}

func TestInboxRetryAndDomainCommandFencing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	server, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range [][]byte{inboxFixture(t), anotherInboxEvent(t, "demo-message-002", driverID)} {
		key := "store-key-001"
		if i == 1 {
			key = "store-key-002"
		}
		status, body := sendInbox(t, server.Handler(), event, "worker-token", key, "1.5")
		if status != http.StatusOK {
			t.Fatalf("store: %d %s", status, body)
		}
	}
	status, body := claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 2)
	first := claimedInbox(t, body).Items[0]
	if status != http.StatusOK || first.Attempt != 1 {
		t.Fatalf("first lease: %d %+v", status, first)
	}
	client := commandClient(t, server)
	_, err = client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "command-key-001", &dataapi.InboxLease{EventID: first.ID, Token: "wrong-token"})
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "LEASE_EXPIRED" {
		t.Fatalf("wrong command lease accepted: %v", err)
	}
	_, err = client.CheckoutCreate(context.Background(), "8000000000000000002", firstVehicleID, 1, "command-key-002", &dataapi.InboxLease{EventID: first.ID, Token: first.LeaseToken})
	if !errors.As(err, &apiErr) || apiErr.Code != "LEASE_EXPIRED" {
		t.Fatalf("other actor used lease: %v", err)
	}
	next := clock.Add(time.Minute)
	status, body = transitionRequest(t, server.Handler(), first.ID, "retry", "retry-key-001", first.LeaseToken, &next)
	if status != http.StatusOK || queueTransition(t, body).State != "retry" {
		t.Fatalf("retry: %d %s", status, body)
	}
	status, body = claimInboxRequest(t, server.Handler(), "claim-key-002", "worker-b", 2)
	if status != http.StatusOK || len(claimedInbox(t, body).Items) != 0 {
		t.Fatalf("later actor event overtook retry: %d %s", status, body)
	}
	clock = next
	status, body = claimInboxRequest(t, server.Handler(), "claim-key-003", "worker-b", 2)
	second := claimedInbox(t, body).Items[0]
	if status != http.StatusOK || second.ID != first.ID || second.Attempt != 2 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("retry lease: %d %+v", status, second)
	}
	status, _ = transitionRequest(t, server.Handler(), first.ID, "ack", "ack-key-001", first.LeaseToken, nil)
	if status != http.StatusConflict {
		t.Fatalf("old worker acknowledged new lease: %d", status)
	}
	_, err = client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "command-key-003", &dataapi.InboxLease{EventID: first.ID, Token: first.LeaseToken})
	if !errors.As(err, &apiErr) || apiErr.Code != "LEASE_EXPIRED" {
		t.Fatalf("old worker mutated domain: %v", err)
	}
	result, err := client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "command-key-004", &dataapi.InboxLease{EventID: second.ID, Token: second.LeaseToken})
	if err != nil || result.Operation != "checkout.create" {
		t.Fatalf("current worker command failed: %+v %v", result, err)
	}
	status, _ = transitionRequest(t, server.Handler(), second.ID, "ack", "ack-key-002", second.LeaseToken, nil)
	if status != http.StatusOK {
		t.Fatalf("current worker ack: %d", status)
	}
	_, err = client.CheckoutCreate(context.Background(), driverID, firstVehicleID, 1, "command-key-004", &dataapi.InboxLease{EventID: second.ID, Token: second.LeaseToken})
	if !errors.As(err, &apiErr) || apiErr.Code != "LEASE_EXPIRED" {
		t.Fatalf("acknowledged lease reused for command: %v", err)
	}
}

func TestInboxAckFailedSnapshotRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "store-key-001", "1.5")
	_, body := claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 1)
	lease := claimedInbox(t, body).Items[0]
	save := server.saveSnapshot
	server.saveSnapshot = func(stateSnapshot) error { return errors.New("disk failed") }
	status, body := transitionRequest(t, server.Handler(), lease.ID, "ack", "ack-key-001", lease.LeaseToken, nil)
	if status != http.StatusServiceUnavailable || len(server.inboxTransitions) != 0 {
		t.Fatalf("failed ack committed: %d %s", status, body)
	}
	server.saveSnapshot = save
	status, body = transitionRequest(t, server.Handler(), lease.ID, "ack", "ack-key-001", lease.LeaseToken, nil)
	if status != http.StatusOK || queueTransition(t, body).State != "done" {
		t.Fatalf("ack after disk recovery: %d %s", status, body)
	}
}

func TestInboxRetryBecomesDeadAfterFiveAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	server, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "store-key-001", "1.5")
	for attempt := 1; attempt <= maxInboxAttempts; attempt++ {
		status, body := claimInboxRequest(t, server.Handler(), fmt.Sprintf("claim-key-%03d", attempt), "worker-a", 1)
		claim := claimedInbox(t, body)
		if status != http.StatusOK || len(claim.Items) != 1 || claim.Items[0].Attempt != attempt {
			t.Fatalf("attempt %d claim: %d %+v", attempt, status, claim)
		}
		lease := claim.Items[0]
		status, body = transitionRequest(t, server.Handler(), lease.ID, "retry", fmt.Sprintf("retry-key-%03d", attempt), lease.LeaseToken, &clock)
		state := queueTransition(t, body).State
		if status != http.StatusOK || (attempt < maxInboxAttempts && state != "retry") || (attempt == maxInboxAttempts && state != "dead") {
			t.Fatalf("attempt %d retry: %d %s", attempt, status, body)
		}
	}
	status, body := claimInboxRequest(t, server.Handler(), "claim-key-final", "worker-b", 1)
	if status != http.StatusOK || len(claimedInbox(t, body).Items) != 0 {
		t.Fatalf("dead event reclaimed: %d %s", status, body)
	}
}

func TestInboxAckLoadsVersionNineSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "store-key-001", "1.5")
	_, body := claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 1)
	lease := claimedInbox(t, body).Items[0]
	previous := server.snapshot()
	previous.Version = 9
	previous.InboxTransitions = nil
	if err := atomicSave(path, previous); err != nil {
		t.Fatal(err)
	}
	restored, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, time.Now)
	if err != nil || restored.inboxTransitions == nil {
		t.Fatalf("v9 snapshot not loaded: %v", err)
	}
	status, body := transitionRequest(t, restored.Handler(), lease.ID, "ack", "ack-key-001", lease.LeaseToken, nil)
	if status != http.StatusOK || queueTransition(t, body).State != "done" {
		t.Fatalf("ack after v9 restart: %d %s", status, body)
	}
}
