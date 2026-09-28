package dataapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkerClientRetriesWithSameIdentityAndNoActorHeader(t *testing.T) {
	var calls int
	var firstRequestID string
	var firstBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/v1/inbox" || r.Header.Get("Authorization") != "Bearer worker-token" || r.Header.Get("X-Actor-Max-ID") != "" || r.Header.Get("Idempotency-Key") != "worker-key-001" || r.Header.Get("X-Contract-Version") != ContractVersion {
			t.Error("worker request used wrong path or headers")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("request body: %v", err)
		}
		if calls == 1 {
			firstRequestID, firstBody = r.Header.Get("X-Request-ID"), string(body)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"DATABASE_UNAVAILABLE","message":"later","retryable":true},"request_id":"` + testRequestID + `"}`))
			return
		}
		if firstRequestID != r.Header.Get("X-Request-ID") || firstBody != string(body) {
			t.Error("retry changed request ID or body")
		}
		_, _ = w.Write([]byte(`{"data":{"id":"` + testRequestID + `","duplicate":true,"stored_at":"2026-09-27T09:00:00Z"},"request_id":"` + testRequestID + `"}`))
	}))
	defer server.Close()
	worker, err := NewWorker(WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	worker.transport.wait = func(context.Context, time.Duration) error { return nil }
	messageID, text := "message-1", "hello"
	event := NormalizedEvent{IntegrationKey: "demo-bot", EventKey: "message:message-1:message_created", EventType: "message_created", ActorMaxUserID: "8000000000000000001", ChatID: "8000000000000000001", MessageID: &messageID, OccurredAt: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC), Payload: NormalizedPayload{Kind: "text", Text: &text}}
	stored, err := worker.StoreInbox(context.Background(), event, "worker-key-001")
	if err != nil || !stored.Duplicate || calls != 2 {
		t.Fatalf("worker retry: %+v %v, calls=%d", stored, err, calls)
	}
}

func TestWorkerClientRejectsInvalidInputsBeforeNetwork(t *testing.T) {
	worker, err := NewWorker(WorkerConfig{BaseURL: "http://127.0.0.1:1/internal/v1", Token: "worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ClaimInbox(context.Background(), "worker-a", 51, "worker-key-001"); err == nil {
		t.Fatal("max_items 51 accepted")
	}
	if _, err := worker.AckInbox(context.Background(), "not-a-uuid", "token", "worker-key-001"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if _, err := worker.RetryInbox(context.Background(), testRequestID, "token", "TEMPORARY_FAILURE", time.Time{}, "worker-key-001"); err == nil {
		t.Fatal("zero next_attempt_at accepted")
	}
	if _, err := worker.GetIntegration(context.Background(), "../other"); err == nil {
		t.Fatal("path traversal integration key accepted")
	}
	if _, err := worker.LeaseIntegration(context.Background(), "demo-bot", "poller-a", 0, "worker-key-001"); err == nil {
		t.Fatal("zero expected version accepted")
	}
	if _, err := worker.CheckpointIntegration(context.Background(), "demo-bot", "lease", 2, nil, "marker", nil, "worker-key-001"); err == nil {
		t.Fatal("missing stored_event_ids array accepted")
	}
	if _, err := worker.CheckpointIntegration(context.Background(), "demo-bot", "lease", 2, nil, "marker", []string{testRequestID, testRequestID}, "worker-key-001"); err == nil {
		t.Fatal("duplicate stored_event_ids accepted")
	}
	if _, err := worker.ClaimNotifications(context.Background(), "worker-a", 51, "worker-key-001"); err == nil {
		t.Fatal("notification max_items 51 accepted")
	}
	if _, err := worker.AckNotification(context.Background(), testRequestID, "lease", "", "worker-key-001"); err == nil {
		t.Fatal("empty provider message ID accepted")
	}
	if _, err := worker.RetryNotification(context.Background(), testRequestID, "lease", "MAX_RATE_LIMIT", nil, false, "worker-key-001"); err == nil {
		t.Fatal("retry without next time accepted")
	}
	next := time.Now().Add(time.Minute)
	if _, err := worker.RetryNotification(context.Background(), testRequestID, "lease", "MAX_RATE_LIMIT", &next, true, "worker-key-001"); err == nil {
		t.Fatal("dead retry with next time accepted")
	}
}
