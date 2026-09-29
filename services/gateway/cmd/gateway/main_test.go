package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
	"github.com/Seferaki/max-fleet/services/gateway/internal/dialog"
	"github.com/Seferaki/max-fleet/services/gateway/internal/notificationworker"
	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
)

func secretFile(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestObserveNotificationsLogsSafeRequestAndQueueMetrics(t *testing.T) {
	oldWriter := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	defer log.SetOutput(oldWriter)

	observeNotifications(notificationworker.Result{
		RequestID:             "10000000-0000-4000-8000-000000000001",
		Claimed:               2,
		Sent:                  1,
		Retried:               1,
		OldestQueueAgeSeconds: 83,
		Failures: []notificationworker.Failure{{
			Operation: "send",
			RequestID: "20000000-0000-4000-8000-000000000002",
			ErrorCode: "MAX_RATE_LIMIT",
		}},
	}, nil)
	got := output.String()
	for _, want := range []string{"operation=send", "request_id=20000000-0000-4000-8000-000000000002", "error_code=MAX_RATE_LIMIT", "claimed=2", "sent=1", "retried=1", "dead=0", "errors=1", "oldest_queue_age_seconds=83"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notification log omitted %q: %s", want, got)
		}
	}
	for _, forbidden := range []string{"8000000000000000001", "Причина:", "private reason", "synthetic delivery id"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("notification log leaked %q: %s", forbidden, got)
		}
	}
}

func TestGatewayWebhookWiringAndMockProductionBan(t *testing.T) {
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", filepath.Join(t.TempDir(), "snapshot.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_API_BASE_URL", server.URL+"/internal/v1")
	t.Setenv("MAX_INTEGRATION_KEY", "")
	t.Setenv("MAX_WEBHOOK_SECRET_FILE", secretFile(t, "webhook", "synthetic-webhook-secret-123"))
	t.Setenv("WORKER_API_TOKEN_FILE", secretFile(t, "worker", "synthetic-worker-token"))
	t.Setenv("DATA_API_TOKEN_FILE", secretFile(t, "actor", "synthetic-service-token"))
	handler, err := webhookHandler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/max/webhook", strings.NewReader(`{"update_type":"message_created","timestamp":1790586000000,"message":{"sender":{"user_id":8000000000000000001},"recipient":{"chat_id":8000000000000000001,"chat_type":"dialog"},"body":{"mid":"gateway-message-1","text":"/start"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(maxbot.SecretHeader, "synthetic-webhook-secret-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("gateway webhook = %d: %s", response.Code, response.Body.String())
	}
	stats := httptest.NewRecorder()
	handler.ServeHTTP(stats, httptest.NewRequest(http.MethodGet, "/health/max-events", nil))
	if stats.Code != http.StatusOK || strings.TrimSpace(stats.Body.String()) != `{"accepted":1,"ignored":0,"rejected":0,"unavailable":0}` {
		t.Fatalf("gateway event counters = %d: %s", stats.Code, stats.Body.String())
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("unfinished gateway claimed ready: %d", ready.Code)
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("MAX_INTEGRATION_KEY", "demo-bot")
	if _, err := webhookHandler(context.Background()); err == nil {
		t.Fatal("production gateway accepted mock DataAPI")
	}
}

func TestGatewayRequiresPrivateWebhookSecretFile(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("MAX_WEBHOOK_SECRET_FILE", "")
	if _, err := webhookHandler(context.Background()); err == nil {
		t.Fatal("webhook started without secret file")
	}
}

func TestPollingModeIsSeparateAndDevelopmentOnly(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_API_BASE_URL", "http://127.0.0.1:8000/internal/v1")
	t.Setenv("MAX_INTEGRATION_KEY", "demo-bot")
	t.Setenv("WORKER_API_TOKEN_FILE", secretFile(t, "worker", "synthetic-worker-token"))
	t.Setenv("MAX_BOT_TOKEN_FILE", secretFile(t, "bot", "synthetic-bot-token"))
	t.Setenv("DATA_API_TOKEN_FILE", secretFile(t, "actor", "synthetic-service-token"))
	handler, runner, err := pollingSetup()
	if err != nil || runner.Source == nil || runner.Store == nil || runner.Reject == nil {
		t.Fatalf("polling setup = %+v, %v", runner, err)
	}
	webhook := httptest.NewRecorder()
	handler.ServeHTTP(webhook, httptest.NewRequest(http.MethodPost, "/max/webhook", nil))
	if webhook.Code != http.StatusNotFound {
		t.Fatalf("polling mode exposed webhook: %d", webhook.Code)
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("polling mode claimed ready: %d", ready.Code)
	}
	t.Setenv("APP_ENV", "production")
	if _, _, err := pollingSetup(); err == nil {
		t.Fatal("polling started in production")
	}
}

func TestInboxWorkerSetupUsesPrivateFilesAndKeepsPartialMode(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("MAX_BOT_TOKEN_FILE", "")
	if _, enabled, err := inboxWorkerSetup(); err != nil || enabled {
		t.Fatalf("missing dev bot token started worker: enabled=%v err=%v", enabled, err)
	}
	t.Setenv("APP_ENV", "production")
	if _, enabled, err := inboxWorkerSetup(); err == nil || enabled {
		t.Fatalf("production started without bot token: enabled=%v err=%v", enabled, err)
	}
	t.Setenv("APP_ENV", "development")
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", filepath.Join(t.TempDir(), "snapshot.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	t.Setenv("DATA_API_BASE_URL", server.URL+"/internal/v1")
	t.Setenv("MAX_BOT_TOKEN_FILE", secretFile(t, "bot", "synthetic-bot-token"))
	t.Setenv("DATA_API_TOKEN_FILE", secretFile(t, "actor", "synthetic-service-token"))
	t.Setenv("WORKER_API_TOKEN_FILE", secretFile(t, "worker", "synthetic-worker-token"))
	worker, enabled, err := inboxWorkerSetup()
	if err != nil || !enabled || worker.Processor == nil || worker.Store == nil {
		t.Fatalf("worker setup = enabled=%v, err=%v", enabled, err)
	}
	bootstrap, ok := worker.Processor.(dialog.Bootstrap)
	if !ok || bootstrap.Location == nil {
		t.Fatal("gateway dialog timezone was not configured")
	}
	t.Setenv("MAX_BOT_NAME", "demo_bot")
	configured, enabled, err := inboxWorkerSetup()
	if err != nil || !enabled || configured.Processor.(dialog.Bootstrap).MapBotName != "demo_bot" {
		t.Fatalf("map launch bot name was not configured: enabled=%v err=%v", enabled, err)
	}
	t.Setenv("MAX_BOT_NAME", "bad/name")
	if _, enabled, err := inboxWorkerSetup(); err == nil || enabled {
		t.Fatal("invalid MAX_BOT_NAME accepted")
	}
	t.Setenv("MAX_BOT_NAME", "")
	result, err := worker.RunOnce(context.Background(), 1)
	if err != nil || result.Claimed != 0 {
		t.Fatalf("empty durable inbox cycle = %+v, %v", result, err)
	}
	ready := httptest.NewRecorder()
	diagnosticsHandler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), "dialog flows incomplete") {
		t.Fatalf("partial readiness = %d: %s", ready.Code, ready.Body.String())
	}
	t.Setenv("COMPANY_TIMEZONE", "invalid/timezone")
	if _, enabled, err := inboxWorkerSetup(); err == nil || enabled {
		t.Fatalf("invalid company timezone accepted: enabled=%v err=%v", enabled, err)
	}
}

func TestMapRoutesRequireTokenAndUseMockActorAPI(t *testing.T) {
	mock, err := datamock.New("synthetic-service-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_API_BASE_URL", server.URL+"/internal/v1")
	t.Setenv("DATA_API_TOKEN_FILE", secretFile(t, "actor", "synthetic-service-token"))
	t.Setenv("MAX_BOT_TOKEN_FILE", "")
	t.Setenv("COMPANY_MAP_LAT", "55.75")
	t.Setenv("COMPANY_MAP_LON", "37.62")
	path := "/api/v1/returns/10000000-0000-4000-8000-000000000001/context"
	mux := diagnosticsHandler()
	if err := registerMapAPI(mux); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("map route enabled without bot token: %d", w.Code)
	}
	const botToken = "synthetic-map-test-token"
	t.Setenv("MAX_BOT_TOKEN_FILE", secretFile(t, "bot", botToken))
	mux = diagnosticsHandler()
	if err := registerMapAPI(mux); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("map route did not check auth: %d", w.Code)
	}
	fields := url.Values{"auth_date": {fmt.Sprint(time.Now().Unix())}, "user": {`{"id":8000000000000000001}`}}
	check := "auth_date=" + fields.Get("auth_date") + "\nuser=" + fields.Get("user")
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = signature.Write([]byte(check))
	fields.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "MaxInitData "+fields.Encode())
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("signed actor did not reach current-return check: %d %s", w.Code, w.Body.String())
	}
	t.Setenv("COMPANY_MAP_LAT", "91")
	if err := registerMapAPI(diagnosticsHandler()); err == nil {
		t.Fatal("invalid map center accepted")
	}
}
