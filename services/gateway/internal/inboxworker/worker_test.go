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

type unstableStore struct {
	*dataapi.WorkerClient
	failClaims int
}

func (s *unstableStore) ClaimInbox(ctx context.Context, workerID string, maxItems int, key string) (dataapi.InboxClaim, error) {
	if s.failClaims > 0 {
		s.failClaims--
		return dataapi.InboxClaim{}, errors.New("synthetic store outage")
	}
	return s.WorkerClient.ClaimInbox(ctx, workerID, maxItems, key)
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

func TestDeferredEventSurvivesLeasesAndRestartWithoutBlockingOtherActor(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	client, server := workerStore(t, path, func() time.Time { return now })
	const actorA = "8000000000000000001"
	const actorB = "8000000000000000002"
	storeStart(t, client, actorA, "start-deferred-1", now)
	storeStart(t, client, actorA, "start-after-deferred", now)
	storeStart(t, client, actorB, "start-other-actor", now)
	var seen []string
	worker := Worker{ID: "deferred-worker", Store: client, Now: func() time.Time { return now }, Processor: processorFunc(func(_ context.Context, item dataapi.InboxClaimItem) error {
		seen = append(seen, item.Event.EventKey)
		if item.Event.EventKey == "start-deferred-1" {
			return ErrDeferred
		}
		return nil
	})}
	first, err := worker.RunOnce(context.Background(), 10)
	if err != nil || first.Claimed != 2 || first.Deferred != 1 || first.Acked != 1 || first.Dead != 0 || len(seen) != 2 || seen[1] != "start-other-actor" {
		t.Fatalf("first batch = %+v, %v; seen=%v", first, err, seen)
	}
	busy, err := worker.RunOnce(context.Background(), 10)
	if err != nil || busy.Claimed != 0 {
		t.Fatalf("deferred lease was reclaimed early: %+v, %v", busy, err)
	}
	for i := 0; i < 6; i++ {
		now = now.Add(3 * time.Minute)
		result, err := worker.RunOnce(context.Background(), 10)
		if err != nil || result.Claimed != 1 || result.Deferred != 1 || result.Dead != 0 || result.Acked != 0 {
			t.Fatalf("deferred lease %d = %+v, %v", i, result, err)
		}
	}
	server.Close()
	client, server = workerStore(t, path, func() time.Time { return now })
	defer server.Close()
	worker.Store = client
	worker.Processor = processorFunc(func(_ context.Context, item dataapi.InboxClaimItem) error {
		seen = append(seen, item.Event.EventKey)
		return nil
	})
	now = now.Add(3 * time.Minute)
	for _, expected := range []string{"start-deferred-1", "start-after-deferred"} {
		result, err := worker.RunOnce(context.Background(), 10)
		if err != nil || result.Claimed != 1 || result.Acked != 1 || seen[len(seen)-1] != expected {
			t.Fatalf("recovered event %s = %+v, %v; seen=%v", expected, result, err, seen)
		}
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

func TestWorkerLoopRecoversFromStoreOutageAndStopsOnCancel(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	client, server := workerStore(t, filepath.Join(t.TempDir(), "snapshot.json"), func() time.Time { return now })
	defer server.Close()
	storeStart(t, client, "8000000000000000001", "start-after-outage", now)
	store := &unstableStore{WorkerClient: client, failClaims: 1}
	processed := 0
	worker := Worker{ID: "inbox-loop-worker", Store: store, Now: func() time.Time { return now }, Processor: processorFunc(func(context.Context, dataapi.InboxClaimItem) error {
		processed++
		return nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observed := 0
	err := worker.Run(ctx, time.Millisecond, 1, func(result Result, err error) {
		observed++
		if observed == 1 && err == nil || observed == 2 && (err != nil || result.Acked != 1) {
			t.Errorf("cycle %d = %+v, %v", observed, result, err)
		}
		if observed == 2 {
			cancel()
		}
	})
	if err != nil || observed != 2 || processed != 1 {
		t.Fatalf("worker shutdown = %v; cycles=%d processed=%d", err, observed, processed)
	}
	claim, err := client.ClaimInbox(context.Background(), "after-loop", 1, "claim-after-loop")
	if err != nil || len(claim.Items) != 0 {
		t.Fatalf("acknowledged event remains in queue: %+v, %v", claim, err)
	}
}
