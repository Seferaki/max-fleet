package inboxworker

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
)

type processorFunc func(context.Context, dataapi.InboxClaimItem) error

func (f processorFunc) Handle(ctx context.Context, item dataapi.InboxClaimItem) error {
	return f(ctx, item)
}

func workerStore(t *testing.T, path string, now func() time.Time) (*dataapi.WorkerClient, *httptest.Server) {
	t.Helper()
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	client, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "synthetic-worker-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return client, server
}

func storeStart(t *testing.T, client *dataapi.WorkerClient, actor, key string, now time.Time) {
	t.Helper()
	event := dataapi.NormalizedEvent{
		IntegrationKey: "demo-bot",
		EventKey:       key,
		EventType:      "bot_started",
		ActorMaxUserID: actor,
		ChatID:         actor,
		OccurredAt:     now.UTC(),
		Payload:        dataapi.NormalizedPayload{Kind: "start"},
	}
	if _, err := client.StoreInbox(context.Background(), event, "store-"+key); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerKeepsActorOrderAndAcksAfterProcessing(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	client, server := workerStore(t, filepath.Join(t.TempDir(), "snapshot.json"), func() time.Time { return now })
	defer server.Close()
	storeStart(t, client, "8000000000000000001", "start-a-1", now)
	storeStart(t, client, "8000000000000000001", "start-a-2", now)
	storeStart(t, client, "8000000000000000002", "start-b-1", now)
	var seen []string
	worker := Worker{ID: "inbox-test-worker", Store: client, Now: func() time.Time { return now }, Processor: processorFunc(func(_ context.Context, item dataapi.InboxClaimItem) error {
		seen = append(seen, item.Event.EventKey)
		return nil
	})}
	first, err := worker.RunOnce(context.Background(), 10)
	if err != nil || first.Claimed != 2 || first.Acked != 2 || len(seen) != 2 || seen[0] != "start-a-1" || seen[1] != "start-b-1" {
		t.Fatalf("first batch = %+v, %v; seen=%v", first, err, seen)
	}
	second, err := worker.RunOnce(context.Background(), 10)
	if err != nil || second.Claimed != 1 || second.Acked != 1 || len(seen) != 3 || seen[2] != "start-a-2" {
		t.Fatalf("second batch = %+v, %v; seen=%v", second, err, seen)
	}
}

func TestWorkerRetryBackoffDeadAndRestart(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	client, server := workerStore(t, path, func() time.Time { return now })
	storeStart(t, client, "8000000000000000001", "start-failing", now)
	worker := Worker{ID: "inbox-test-worker", Store: client, Now: func() time.Time { return now }, Processor: processorFunc(func(context.Context, dataapi.InboxClaimItem) error {
		return &ProcessError{Code: "TEMPORARY_FAILURE", Err: errors.New("synthetic failure")}
	})}
	for attempt := 1; attempt <= 5; attempt++ {
		result, err := worker.RunOnce(context.Background(), 10)
		if err != nil || result.Claimed != 1 || result.Acked != 0 || attempt < 5 && result.Retried != 1 || attempt == 5 && result.Dead != 1 {
			t.Fatalf("attempt %d = %+v, %v", attempt, result, err)
		}
		if attempt == 2 {
			server.Close()
			client, server = workerStore(t, path, func() time.Time { return now })
			worker.Store = client
		}
		now = now.Add(backoff(attempt))
	}
	defer server.Close()
	final, err := worker.RunOnce(context.Background(), 10)
	if err != nil || final.Claimed != 0 {
		t.Fatalf("dead event was reclaimed: %+v, %v", final, err)
	}
}

func TestWorkerCannotAckExpiredLease(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	client, server := workerStore(t, filepath.Join(t.TempDir(), "snapshot.json"), func() time.Time { return now })
	defer server.Close()
	storeStart(t, client, "8000000000000000001", "start-expired", now)
	worker := Worker{ID: "inbox-test-worker", Store: client, Now: func() time.Time { return now }, Processor: processorFunc(func(context.Context, dataapi.InboxClaimItem) error {
		now = now.Add(3 * time.Minute)
		return nil
	})}
	if _, err := worker.RunOnce(context.Background(), 10); err == nil {
		t.Fatal("expired lease was acknowledged")
	}
}

func TestWorkerRetryAfterDomainCommitReusesCommandKey(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	workerClient, server := workerStore(t, path, func() time.Time { return now })
	defer server.Close()
	actorClient, err := dataapi.New(dataapi.Config{BaseURL: server.URL + "/internal/v1", Token: "synthetic-service-token"})
	if err != nil {
		t.Fatal(err)
	}
	const actor = "8000000000000000001"
	storeStart(t, workerClient, actor, "start-domain-retry", now)
	available := true
	vehicles, err := actorClient.Vehicles(context.Background(), actor, dataapi.VehicleFilter{Available: &available})
	if err != nil || len(vehicles.Items) == 0 {
		t.Fatalf("available vehicles = %+v, %v", vehicles, err)
	}
	vehicle := vehicles.Items[0]
	var checkoutIDs, commandKeys []string
	attempt := 0
	worker := Worker{ID: "inbox-domain-worker", Store: workerClient, Now: func() time.Time { return now }, Processor: processorFunc(func(ctx context.Context, item dataapi.InboxClaimItem) error {
		attempt++
		key, err := CommandKey(item, "checkout.create")
		if err != nil {
			return err
		}
		commandKeys = append(commandKeys, key)
		result, err := actorClient.CheckoutCreate(ctx, actor, vehicle.ID, vehicle.Version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
		if err != nil {
			return err
		}
		checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
		if err != nil {
			return err
		}
		checkoutIDs = append(checkoutIDs, checkout.ID)
		if attempt == 1 {
			return errors.New("synthetic crash after domain commit")
		}
		return nil
	})}
	first, err := worker.RunOnce(context.Background(), 1)
	if err != nil || first.Retried != 1 {
		t.Fatalf("post-commit retry = %+v, %v", first, err)
	}
	now = now.Add(backoff(1))
	second, err := worker.RunOnce(context.Background(), 1)
	if err != nil || second.Acked != 1 || len(checkoutIDs) != 2 || checkoutIDs[0] != checkoutIDs[1] || commandKeys[0] != commandKeys[1] {
		t.Fatalf("domain replay = %+v, %v; checkouts=%v keys=%v", second, err, checkoutIDs, commandKeys)
	}
	summary, err := actorClient.AdminSummary(context.Background(), "8000000000000000003")
	if err != nil || summary.Holding != 1 {
		t.Fatalf("duplicate hold after replay: %+v, %v", summary, err)
	}
}
