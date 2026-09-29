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

func inboxFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contracts", "examples", "inbox-photo.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func sendInbox(t *testing.T, handler http.Handler, body []byte, token, key, version string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/inbox", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("X-Contract-Version", version)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

func storedInbox(t *testing.T, body []byte) dataapi.InboxStored {
	t.Helper()
	var envelope struct {
		Data dataapi.InboxStored `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || !validUUID(envelope.Data.ID) {
		t.Fatalf("invalid inbox response: %s, %v", body, err)
	}
	return envelope.Data
}

func TestInboxWorkerAuthIdempotencyAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	fixture := inboxFixture(t)
	for _, token := range []string{"service-token", "wrong-token"} {
		status, _ := sendInbox(t, server.Handler(), fixture, token, "inbox-key-001", "1.7")
		if status != http.StatusUnauthorized {
			t.Fatalf("%s accepted: %d", token, status)
		}
	}
	status, _ := sendInbox(t, server.Handler(), fixture, "worker-token", "", "1.7")
	if status != http.StatusBadRequest {
		t.Fatalf("missing idempotency key: %d", status)
	}
	status, _ = sendInbox(t, server.Handler(), fixture, "worker-token", "inbox-key-001", "2.0")
	if status != http.StatusBadRequest {
		t.Fatalf("wrong version: %d", status)
	}
	status, response := sendInbox(t, server.Handler(), fixture, "worker-token", "inbox-key-001", "1.7")
	first := storedInbox(t, response)
	if status != http.StatusOK || first.Duplicate || !first.StoredAt.Equal(clock) {
		t.Fatalf("first store: status=%d data=%+v", status, first)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("inbox acknowledged before snapshot: %v", err)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"inbox-key-001", "inbox-key-002"} {
		status, response = sendInbox(t, restarted.Handler(), fixture, "worker-token", key, "1.7")
		duplicate := storedInbox(t, response)
		if status != http.StatusOK || !duplicate.Duplicate || duplicate.ID != first.ID || !duplicate.StoredAt.Equal(first.StoredAt) {
			t.Fatalf("duplicate after restart: status=%d data=%+v", status, duplicate)
		}
	}
	if len(restarted.inbox) != 1 || len(restarted.inboxKeys) != 2 {
		t.Fatalf("unexpected durable indexes: inbox=%d keys=%d", len(restarted.inbox), len(restarted.inboxKeys))
	}
	var changed map[string]any
	if err := json.Unmarshal(fixture, &changed); err != nil {
		t.Fatal(err)
	}
	changed["event_key"] = "message:other:message_created"
	changed["message_id"] = "other"
	modified, _ := json.Marshal(changed)
	status, _ = sendInbox(t, restarted.Handler(), modified, "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusConflict {
		t.Fatalf("same key, different event: %d", status)
	}
	changed["event_key"] = "message:demo-message-001:message_created"
	changed["message_id"] = "demo-message-001"
	changed["payload"].(map[string]any)["photo_source_key"] = "other-photo"
	modified, _ = json.Marshal(changed)
	status, _ = sendInbox(t, restarted.Handler(), modified, "worker-token", "inbox-key-003", "1.7")
	if status != http.StatusConflict || len(restarted.inbox) != 1 {
		t.Fatalf("same event key, different body: %d", status)
	}
}

func TestInboxFailedSaveDoesNotAcknowledgeOrChangeMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	save := server.saveSnapshot
	server.saveSnapshot = func(stateSnapshot) error { return errors.New("disk failed") }
	status, body := sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusServiceUnavailable || len(server.inbox) != 0 || len(server.inboxKeys) != 0 {
		t.Fatalf("failed save acknowledged or changed memory: %d %s", status, body)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed save created snapshot: %v", err)
	}
	server.saveSnapshot = save
	status, body = sendInbox(t, server.Handler(), inboxFixture(t), "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusOK || storedInbox(t, body).Duplicate {
		t.Fatalf("retry after disk recovery: %d %s", status, body)
	}
	withoutSnapshot, err := newServer("service-token", "worker-token", "", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	status, _ = sendInbox(t, withoutSnapshot.Handler(), inboxFixture(t), "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusServiceUnavailable || len(withoutSnapshot.inbox) != 0 {
		t.Fatalf("in-memory mock acknowledged non-durable event: %d", status)
	}
}

func TestInboxLoadsPreviousSnapshotVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	previous := server.snapshot()
	previous.Version = 7
	previous.Inbox = nil
	previous.InboxKeys = nil
	if err := atomicSave(path, previous); err != nil {
		t.Fatal(err)
	}
	restored, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil || restored.inbox == nil || restored.inboxKeys == nil {
		t.Fatalf("v7 snapshot not upgraded in memory: %v", err)
	}
	status, body := sendInbox(t, restored.Handler(), inboxFixture(t), "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusOK || storedInbox(t, body).Duplicate {
		t.Fatalf("inbox after v7 restart: %d %s", status, body)
	}
}

func TestInboxRejectsMalformedAndMultiplePhotos(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(inboxFixture(t), &event); err != nil {
		t.Fatal(err)
	}
	event["payload"].(map[string]any)["attachment_count"] = float64(2)
	body, _ := json.Marshal(event)
	status, _ := sendInbox(t, server.Handler(), body, "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusBadRequest {
		t.Fatalf("two photos accepted: %d", status)
	}
	delete(event, "callback_id")
	body, _ = json.Marshal(event)
	status, _ = sendInbox(t, server.Handler(), body, "worker-token", "inbox-key-001", "1.7")
	if status != http.StatusBadRequest || len(server.inbox) != 0 {
		t.Fatalf("missing nullable field accepted: %d", status)
	}
}
