package dataapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssetContentRetriesAndValidatesPrivateResponse(t *testing.T) {
	const assetID = "10000000-0000-4000-8000-000000000001"
	requestID := ""
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/v1/assets/"+assetID+"/content" || r.Header.Get("X-Actor-Max-ID") != "8000000000000000001" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Contract-Version") != ContractVersion {
			t.Error("private asset headers missing")
		}
		if requestID == "" {
			requestID = r.Header.Get("X-Request-ID")
		} else if requestID != r.Header.Get("X-Request-ID") {
			t.Error("retry changed request ID")
		}
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"TEMPORARY_FAILURE","message":"retry","retryable":true},"request_id":"10000000-0000-4000-8000-000000000001"}`))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("synthetic-image"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/internal/v1", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	client.wait = func(context.Context, time.Duration) error { return nil }
	result, err := client.AssetContent(context.Background(), "8000000000000000001", assetID)
	if err != nil || result.ContentType != "image/png" || string(result.Bytes) != "synthetic-image" || calls != 2 {
		t.Fatalf("asset retry: %+v %v calls=%d", result, err, calls)
	}
}

func TestAssetContentRejectsOversizeAndWrongMedia(t *testing.T) {
	contentType := "text/plain"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if contentType == "image/png" {
			_, _ = w.Write([]byte(strings.Repeat("x", (10<<20)+1)))
			return
		}
		_, _ = w.Write([]byte("not image"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/internal/v1", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, media := range []string{"text/plain", "image/png"} {
		contentType = media
		if _, err := client.AssetContent(context.Background(), "8000000000000000001", "10000000-0000-4000-8000-000000000001"); err == nil {
			t.Fatalf("accepted %s", media)
		}
	}
	_, err = client.AssetContent(context.Background(), "bad", "10000000-0000-4000-8000-000000000001")
	if err == nil {
		t.Fatal("invalid actor accepted")
	}
}
