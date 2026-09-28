package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
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
