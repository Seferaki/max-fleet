package datamock

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func claimInboxRequest(t *testing.T, handler http.Handler, key, worker string, maxItems int) (int, []byte) {
	t.Helper()
	input, err := json.Marshal(map[string]any{"worker_id": worker, "max_items": maxItems})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/inbox/claim", bytes.NewReader(input))
	req.Header.Set("Authorization", "Bearer worker-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("X-Contract-Version", "1.2")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	response := w.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func claimedInbox(t *testing.T, body []byte) dataapi.InboxClaim {
	t.Helper()
	var envelope struct {
		Data dataapi.InboxClaim `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.Items == nil {
		t.Fatalf("invalid claim response: %s %v", body, err)
	}
	return envelope.Data
}

func anotherInboxEvent(t *testing.T, messageID, actor string) []byte {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(inboxFixture(t), &event); err != nil {
		t.Fatal(err)
	}
	event["message_id"] = messageID
	event["event_key"] = "message:" + messageID + ":message_created"
	event["actor_max_user_id"] = actor
	event["chat_id"] = actor
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestInboxClaimPreservesActorOrderAndFencesExpiredLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	first, second, third := inboxFixture(t), anotherInboxEvent(t, "demo-message-002", driverID), anotherInboxEvent(t, "demo-message-003", "8000000000000000002")
	storedIDs := make([]string, 0, 3)
	for i, body := range [][]byte{first, second, third} {
		status, response := sendInbox(t, server.Handler(), body, "worker-token", "store-key-00"+string(rune('1'+i)), "1.2")
		if status != http.StatusOK {
			t.Fatalf("store %d: %d %s", i, status, response)
		}
		storedIDs = append(storedIDs, storedInbox(t, response).ID)
	}
	status, body := claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 3)
	claimed := claimedInbox(t, body)
	if status != http.StatusOK || len(claimed.Items) != 2 || claimed.Items[0].ID != storedIDs[0] || claimed.Items[1].ID != storedIDs[2] || claimed.Items[0].Attempt != 1 || claimed.Items[0].LeaseToken == "" {
		t.Fatalf("actor ordering: %d %+v", status, claimed)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	status, body = claimInboxRequest(t, restarted.Handler(), "claim-key-001", "worker-a", 3)
	replayed := claimedInbox(t, body)
	if status != http.StatusOK || len(replayed.Items) != 2 || replayed.Items[0].LeaseToken != claimed.Items[0].LeaseToken || replayed.Items[1].LeaseToken != claimed.Items[1].LeaseToken {
		t.Fatalf("claim replay after restart: %d %+v", status, replayed)
	}
	status, body = claimInboxRequest(t, restarted.Handler(), "claim-key-002", "worker-b", 3)
	if status != http.StatusOK || len(claimedInbox(t, body).Items) != 0 {
		t.Fatalf("active lease claimed twice: %d %s", status, body)
	}
	clock = clock.Add(inboxLeaseDuration + time.Second)
	status, body = claimInboxRequest(t, restarted.Handler(), "claim-key-003", "worker-b", 3)
	afterExpiry := claimedInbox(t, body)
	if status != http.StatusOK || len(afterExpiry.Items) != 2 || afterExpiry.Items[0].ID != storedIDs[0] || afterExpiry.Items[0].Attempt != 2 || afterExpiry.Items[0].LeaseToken == claimed.Items[0].LeaseToken {
		t.Fatalf("expired lease not fenced: %d %+v", status, afterExpiry)
	}
	status, _ = claimInboxRequest(t, restarted.Handler(), "claim-key-001", "worker-a", 3)
	if status != http.StatusConflict {
		t.Fatalf("expired idempotent claim replay accepted: %d", status)
	}
	status, _ = claimInboxRequest(t, restarted.Handler(), "claim-key-003", "worker-other", 3)
	if status != http.StatusConflict {
		t.Fatalf("same claim key with changed body accepted: %d", status)
	}
	status, _ = claimInboxRequest(t, restarted.Handler(), "claim-key-004", "worker-b", 51)
	if status != http.StatusBadRequest {
		t.Fatalf("max_items 51 accepted: %d", status)
	}
}

func TestInboxClaimSaveFailureRollsBackLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	status, response := sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "store-key-001", "1.2")
	if status != http.StatusOK {
		t.Fatalf("store: %d %s", status, response)
	}
	save := server.saveSnapshot
	server.saveSnapshot = func(stateSnapshot) error { return errors.New("disk failed") }
	status, response = claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 1)
	if status != http.StatusServiceUnavailable || len(server.inboxClaims) != 0 {
		t.Fatalf("failed claim was acknowledged: %d %s", status, response)
	}
	for _, event := range server.inbox {
		if event.Status != "pending" || event.Attempt != 0 || event.LeaseToken != "" {
			t.Fatalf("lease survived failed snapshot: %+v", event)
		}
	}
	server.saveSnapshot = save
	status, response = claimInboxRequest(t, server.Handler(), "claim-key-001", "worker-a", 1)
	if status != http.StatusOK || len(claimedInbox(t, response).Items) != 1 {
		t.Fatalf("claim after storage recovery: %d %s", status, response)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot missing after claim: %v", err)
	}
}

func TestInboxClaimUpgradesVersionEightSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	status, response := sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "store-key-001", "1.2")
	if status != http.StatusOK {
		t.Fatalf("store: %d %s", status, response)
	}
	previous := server.snapshot()
	previous.Version = 8
	previous.InboxSequence = 0
	previous.InboxClaims = nil
	for key, event := range previous.Inbox {
		event.Sequence = 0
		previous.Inbox[key] = event
	}
	if err := atomicSave(path, previous); err != nil {
		t.Fatal(err)
	}
	restored, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil || restored.inboxSequence != 1 {
		t.Fatalf("v8 sequence not reconstructed: %v", err)
	}
	status, response = claimInboxRequest(t, restored.Handler(), "claim-key-001", "worker-a", 1)
	if status != http.StatusOK || len(claimedInbox(t, response).Items) != 1 {
		t.Fatalf("claim after v8 restart: %d %s", status, response)
	}
}
