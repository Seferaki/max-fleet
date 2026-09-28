package dataapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testRequestID = "11111111-1111-4111-8111-111111111111"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{BaseURL: server.URL + "/internal/v1", Token: "private-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	c.wait = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestUnknownActorHasOnlyOwnID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/me" || r.Header.Get("Authorization") != "Bearer private-test-token" || r.Header.Get("X-Actor-Max-ID") != "900001" || r.Header.Get("X-Contract-Version") != ContractVersion || !validUUID(r.Header.Get("X-Request-ID")) {
			t.Errorf("incorrect request path or mandatory headers")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"allowed":false,"max_user_id":"900001","employee":null},"request_id":"` + testRequestID + `"}`))
	})
	me, err := c.Me(context.Background(), "900001")
	if err != nil || me.Allowed || me.Employee != nil || me.MaxUserID != "900001" {
		t.Fatalf("unexpected unknown actor response: %+v, %v", me, err)
	}
}

func TestRetry503KeepsRequestID(t *testing.T) {
	var calls int
	var firstID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			firstID = r.Header.Get("X-Request-ID")
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"TEMPORARY_FAILURE","message":"later","retryable":true},"request_id":"` + testRequestID + `"}`))
			return
		}
		if r.Header.Get("X-Request-ID") != firstID {
			t.Error("retry changed request ID")
		}
		_, _ = w.Write([]byte(`{"data":{"contract_version":"1.2","build_sha":"test","mode":"mock","capabilities":[]},"request_id":"` + testRequestID + `"}`))
	})
	meta, err := c.Meta(context.Background())
	if err != nil || calls != 2 || meta.Mode != "mock" {
		t.Fatalf("retry result: calls=%d meta=%+v err=%v", calls, meta, err)
	}
}

func Test409ReturnsTypedErrorWithoutRetry(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"STALE_VERSION","message":"stale","retryable":false,"details":{"current_version":4}},"request_id":"` + testRequestID + `"}`))
	})
	_, err := c.Vehicle(context.Background(), "900001", testRequestID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "STALE_VERSION" || apiErr.Details.CurrentVersion == nil || *apiErr.Details.CurrentVersion != 4 || calls != 1 {
		t.Fatalf("unexpected error: %v, calls=%d", err, calls)
	}
	if strings.Contains(err.Error(), "private-test-token") {
		t.Fatal("secret leaked in error")
	}
}

func TestInvalidInputNeverSent(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid request reached server")
	})
	if _, err := c.Me(context.Background(), "not-a-max-id"); err == nil {
		t.Fatal("invalid actor accepted")
	}
	if _, err := c.Vehicle(context.Background(), "900001", "../admin"); err == nil {
		t.Fatal("invalid vehicle ID accepted")
	}
	if _, err := c.Vehicles(context.Background(), "900001", VehicleFilter{Limit: 51}); err == nil {
		t.Fatal("limit 51 accepted")
	}
	if _, err := New(Config{BaseURL: "https://user:password@example.test/internal/v1", Token: "secret"}); err == nil {
		t.Fatal("URL with credentials accepted")
	}
}

func TestUnknownResponseFieldRejected(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"allowed":false,"max_user_id":"900001","employee":null,"private_data":"leak"},"request_id":"` + testRequestID + `"}`))
	})
	if _, err := c.Me(context.Background(), "900001"); err == nil || !strings.Contains(err.Error(), "invalid success response") {
		t.Fatalf("unexpected response accepted: %v", err)
	}
}

func TestMetadataRejectsIncompatibleContract(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"contract_version":"2.0","build_sha":"test","mode":"mock","capabilities":[]},"request_id":"` + testRequestID + `"}`))
	})
	if _, err := c.Meta(context.Background()); err == nil || !strings.Contains(err.Error(), "incompatible metadata") {
		t.Fatalf("incompatible contract accepted: %v", err)
	}
}
