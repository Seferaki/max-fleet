package datamock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestWorkerClientAgainstSeparateMock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshotAndWorkerToken("service-token", "worker-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	var event dataapi.NormalizedEvent
	if err := json.Unmarshal(inboxFixture(t), &event); err != nil {
		t.Fatal(err)
	}
	stored, err := worker.StoreInbox(context.Background(), event, "store-key-001")
	if err != nil || stored.Duplicate {
		t.Fatalf("store: %+v %v", stored, err)
	}
	claim, err := worker.ClaimInbox(context.Background(), "worker-a", 1, "claim-key-001")
	if err != nil || len(claim.Items) != 1 || claim.Items[0].ID != stored.ID {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	ack, err := worker.AckInbox(context.Background(), stored.ID, claim.Items[0].LeaseToken, "ack-key-001")
	if err != nil || ack.State != "done" {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	if err := json.Unmarshal(anotherInboxEvent(t, "demo-message-002", driverID), &event); err != nil {
		t.Fatal(err)
	}
	stored, err = worker.StoreInbox(context.Background(), event, "store-key-002")
	if err != nil {
		t.Fatal(err)
	}
	claim, err = worker.ClaimInbox(context.Background(), "worker-a", 1, "claim-key-002")
	if err != nil || len(claim.Items) != 1 || claim.Items[0].ID != stored.ID {
		t.Fatalf("second claim: %+v %v", claim, err)
	}
	retry, err := worker.RetryInbox(context.Background(), stored.ID, claim.Items[0].LeaseToken, "TEMPORARY_FAILURE", clock.Add(time.Minute), "retry-key-001")
	if err != nil || retry.State != "retry" {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	wrong, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "service-token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrong.ClaimInbox(context.Background(), "worker-b", 1, "claim-key-003")
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("service token accepted by worker route: %v", err)
	}
}
