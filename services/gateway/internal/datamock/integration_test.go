package datamock

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func integrationRequest(t *testing.T, handler http.Handler, method, path, token, version, key string, value any) (int, []byte) {
	t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("X-Contract-Version", version)
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	response := w.Result()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func integrationData[T any](t *testing.T, body []byte) T {
	t.Helper()
	var response struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func integrationErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Error.Code
}

func TestIntegrationLeaseAuthReplayExpiryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	base := "/internal/v1/integrations/demo-bot"
	for _, token := range []string{"service-token", "wrong-token"} {
		status, _ := integrationRequest(t, server.Handler(), http.MethodGet, base, token, "1.0", "", nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("non-worker auth: %d", status)
		}
	}
	status, _ := integrationRequest(t, server.Handler(), http.MethodGet, base, "worker-token", "2.0", "", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("wrong contract version: %d", status)
	}
	status, body := integrationRequest(t, server.Handler(), http.MethodGet, base, "worker-token", "1.0", "", nil)
	initial := integrationData[dataapi.Integration](t, body)
	if status != http.StatusOK || initial.Key != "demo-bot" || initial.Mode != "polling" || initial.Marker != nil || initial.LeaseExpiresAt != nil || initial.Version != 1 {
		t.Fatalf("initial integration: %d %+v", status, initial)
	}
	status, _ = integrationRequest(t, server.Handler(), http.MethodGet, "/internal/v1/integrations/unknown", "worker-token", "1.0", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("unknown integration: %d", status)
	}
	leasePath := base + "/lease"
	workerA := map[string]any{"worker_id": "poller-a", "expected_version": 1}
	status, body = integrationRequest(t, server.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-001", workerA)
	first := integrationData[dataapi.IntegrationLease](t, body)
	if status != http.StatusOK || first.LeaseToken == "" || first.Integration.Version != 2 || !first.LeaseExpiresAt.Equal(clock.Add(integrationLeaseDuration)) {
		t.Fatalf("first lease: %d %+v", status, first.Integration)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-001", workerA)
	replayed := integrationData[dataapi.IntegrationLease](t, body)
	if status != http.StatusOK || replayed.LeaseToken != first.LeaseToken || replayed.Integration.Version != 2 {
		t.Fatalf("restart replay: %d %+v", status, replayed.Integration)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-001", map[string]any{"worker_id": "poller-b", "expected_version": 1})
	if status != http.StatusConflict || integrationErrorCode(t, body) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("changed replay accepted: %d", status)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-002", map[string]any{"worker_id": "poller-b", "expected_version": 2})
	if status != http.StatusConflict || integrationErrorCode(t, body) != "COMMAND_IN_PROGRESS" {
		t.Fatalf("second poller accepted: %d", status)
	}
	clock = clock.Add(time.Minute)
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-003", map[string]any{"worker_id": "poller-a", "expected_version": 2})
	renewed := integrationData[dataapi.IntegrationLease](t, body)
	if status != http.StatusOK || renewed.LeaseToken != first.LeaseToken || renewed.Integration.Version != 3 || !renewed.LeaseExpiresAt.Equal(clock.Add(integrationLeaseDuration)) {
		t.Fatalf("renewal: %d %+v", status, renewed.Integration)
	}
	clock = clock.Add(integrationLeaseDuration + time.Second)
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-003", map[string]any{"worker_id": "poller-a", "expected_version": 2})
	if status != http.StatusConflict || integrationErrorCode(t, body) != "LEASE_EXPIRED" {
		t.Fatalf("expired replay accepted: %d", status)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-004", map[string]any{"worker_id": "poller-b", "expected_version": 2})
	if status != http.StatusConflict || integrationErrorCode(t, body) != "STALE_VERSION" {
		t.Fatalf("stale version accepted: %d", status)
	}
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, leasePath, "worker-token", "1.0", "lease-key-005", map[string]any{"worker_id": "poller-b", "expected_version": 3})
	afterExpiry := integrationData[dataapi.IntegrationLease](t, body)
	if status != http.StatusOK || afterExpiry.LeaseToken == first.LeaseToken || afterExpiry.Integration.Version != 4 {
		t.Fatalf("expired lease not fenced: %d %+v", status, afterExpiry.Integration)
	}
}

func TestIntegrationLeaseSaveFailureAndV10Upgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	previous := server.snapshot()
	previous.Version = 10
	previous.Integrations = nil
	previous.IntegrationLeases = nil
	if err := atomicSave(path, previous); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, time.Now)
	if err != nil || restarted.integrations["demo-bot"].Data.Version != 1 {
		t.Fatalf("v10 upgrade: %v", err)
	}
	save := restarted.saveSnapshot
	restarted.saveSnapshot = func(stateSnapshot) error { return errors.New("synthetic disk failure") }
	status, body := integrationRequest(t, restarted.Handler(), http.MethodPost, "/internal/v1/integrations/demo-bot/lease", "worker-token", "1.0", "lease-key-001", map[string]any{"worker_id": "poller-a", "expected_version": 1})
	if status != http.StatusServiceUnavailable || integrationErrorCode(t, body) != "DATABASE_UNAVAILABLE" || restarted.integrations["demo-bot"].Data.Version != 1 || len(restarted.integrationLeases) != 0 {
		t.Fatalf("failed snapshot acknowledged lease: %d", status)
	}
	restarted.saveSnapshot = save
	status, body = integrationRequest(t, restarted.Handler(), http.MethodPost, "/internal/v1/integrations/demo-bot/lease", "worker-token", "1.0", "lease-key-001", map[string]any{"worker_id": "poller-a", "expected_version": 1})
	if status != http.StatusOK || integrationData[dataapi.IntegrationLease](t, body).Integration.Version != 2 {
		t.Fatalf("lease after storage recovery: %d", status)
	}
}
