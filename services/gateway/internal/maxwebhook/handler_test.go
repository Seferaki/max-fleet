package maxwebhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
)

const (
	testSecret = "synthetic-webhook-secret-123"
	photoJSON  = `{"update_type":"message_created","timestamp":1790586000000,"message":{"sender":{"user_id":8000000000000000001},"recipient":{"chat_id":8000000000000000001,"chat_type":"dialog"},"body":{"mid":"webhook-photo-1","attachments":[{"type":"image","payload":{"token":"synthetic-photo-source"}}]}}}`
)

type recordingStore struct {
	calls int
	event dataapi.NormalizedEvent
	key   string
	err   error
}

func (s *recordingStore) StoreInbox(_ context.Context, event dataapi.NormalizedEvent, key string) (dataapi.InboxStored, error) {
	s.calls++
	s.event = event
	s.key = key
	return dataapi.InboxStored{}, s.err
}

func postWebhook(handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/max/webhook", strings.NewReader(body))
	request.Header.Set(maxbot.SecretHeader, secret)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestWebhookChecksAuthBodyAndMediaBeforeStore(t *testing.T) {
	store := &recordingStore{}
	handler, err := New(testSecret, "demo-bot", store)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		secret string
		body   string
		status int
	}{
		{"wrong-secret", photoJSON, http.StatusUnauthorized},
		{testSecret, "{", http.StatusBadRequest},
		{testSecret, strings.Repeat("x", maxBodyBytes+1), http.StatusRequestEntityTooLarge},
		{testSecret, strings.Replace(photoJSON, "webhook-photo-1", "", 1), http.StatusBadRequest},
	} {
		response := postWebhook(handler, test.secret, test.body)
		if response.Code != test.status {
			t.Fatalf("response = %d; expected %d", response.Code, test.status)
		}
	}
	if store.calls != 0 {
		t.Fatalf("invalid request reached inbox %d times", store.calls)
	}
	request := httptest.NewRequest(http.MethodPost, "/max/webhook", strings.NewReader(photoJSON))
	request.Header.Set(maxbot.SecretHeader, testSecret)
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.calls != 0 {
		t.Fatalf("wrong media type = %d; calls=%d", response.Code, store.calls)
	}
}

func TestWebhookRejectsMultiPhotoAndIgnoresGroup(t *testing.T) {
	store := &recordingStore{}
	handler, _ := New(testSecret, "demo-bot", store)
	multi := strings.Replace(photoJSON, `{"type":"image","payload":{"token":"synthetic-photo-source"}}`, `{"type":"image","payload":{"token":"one"}},{"type":"image","payload":{"token":"two"}}`, 1)
	if response := postWebhook(handler, testSecret, multi); response.Code != http.StatusBadRequest {
		t.Fatalf("multi photo = %d", response.Code)
	}
	group := strings.Replace(photoJSON, `"chat_type":"dialog"`, `"chat_type":"chat"`, 1)
	if response := postWebhook(handler, testSecret, group); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "IGNORED") {
		t.Fatalf("group = %d, %s", response.Code, response.Body.String())
	}
	if response := postWebhook(handler, testSecret, `{"update_type":"future_event","timestamp":1790586000000}`); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "IGNORED") {
		t.Fatalf("unknown event = %d, %s", response.Code, response.Body.String())
	}
	if store.calls != 0 {
		t.Fatalf("rejected event reached inbox %d times", store.calls)
	}
}

func TestWebhookAcknowledgesOnlyAfterDurableMockStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "synthetic-worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(testSecret, "demo-bot", worker)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if response := postWebhook(handler, testSecret, photoJSON); response.Code != http.StatusOK {
			t.Fatalf("webhook attempt %d = %d: %s", i, response.Code, response.Body.String())
		}
	}
	server.Close()
	restarted, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(restarted.Handler())
	defer server.Close()
	worker, err = dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "synthetic-worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := worker.ClaimInbox(context.Background(), "webhook-test", 10, "claim-webhook-test")
	if err != nil || len(claimed.Items) != 1 {
		t.Fatalf("durable inbox after restart = %+v, %v", claimed, err)
	}
	event := claimed.Items[0].Event
	if event.EventKey != "message:webhook-photo-1:message_created" || event.ActorMaxUserID != "8000000000000000001" || event.Payload.Kind != "photo" {
		t.Fatalf("wrong stored event = %+v", event)
	}
}

func TestWebhookStoreFailureReturns503WithoutSuccess(t *testing.T) {
	store := &recordingStore{err: errors.New("snapshot unavailable")}
	handler, _ := New(testSecret, "demo-bot", store)
	response := postWebhook(handler, testSecret, photoJSON)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "STORED") || store.calls != 1 {
		t.Fatalf("store failure = %d, %s; calls=%d", response.Code, response.Body.String(), store.calls)
	}
}

func TestWebhookStatsCountOutcomesWithoutMessageData(t *testing.T) {
	store := &recordingStore{}
	handler, err := New(testSecret, "demo-bot", store)
	if err != nil {
		t.Fatal(err)
	}
	group := strings.Replace(photoJSON, `"chat_type":"dialog"`, `"chat_type":"chat"`, 1)
	for _, body := range []string{group, `{"update_type":"future_event","timestamp":1790586000000}`} {
		if response := postWebhook(handler, testSecret, body); response.Code != http.StatusOK {
			t.Fatalf("ignored event response = %d", response.Code)
		}
	}
	store.err = errors.New("synthetic unavailable")
	if response := postWebhook(handler, testSecret, photoJSON); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable response = %d", response.Code)
	}
	store.err = nil
	if response := postWebhook(handler, testSecret, photoJSON); response.Code != http.StatusOK {
		t.Fatalf("accepted response = %d", response.Code)
	}
	if stats := handler.Stats(); stats.Accepted != 1 || stats.Ignored != 2 || stats.Unavailable != 1 {
		t.Fatalf("wrong webhook counters: %+v", stats)
	}
}
