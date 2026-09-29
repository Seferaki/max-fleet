package datamock

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/notificationworker"
)

type notificationSenderStub struct {
	calls []string
	err   error
}

func (s *notificationSenderStub) SendText(_ context.Context, _ int64, text string) (string, error) {
	s.calls = append(s.calls, text)
	if s.err != nil {
		return "", s.err
	}
	return fmt.Sprintf("max-message-%d", len(s.calls)), nil
}

type notificationRateLimit struct{}

func (notificationRateLimit) Error() string                 { return "provider rate limit" }
func (notificationRateLimit) NotificationErrorCode() string { return "MAX_RATE_LIMIT" }

type failNotificationAckOnce struct {
	notificationworker.Store
	fail bool
}

func (s *failNotificationAckOnce) AckNotification(ctx context.Context, id, leaseToken, providerMessageID, key string) (dataapi.QueueTransition, error) {
	if s.fail {
		s.fail = false
		return dataapi.QueueTransition{}, errors.New("synthetic acknowledgement timeout")
	}
	return s.Store.AckNotification(ctx, id, leaseToken, providerMessageID, key)
}

func notificationWorkerTestServer(t *testing.T, path string, now func() time.Time) (*Server, *httptest.Server, *dataapi.WorkerClient) {
	t.Helper()
	mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", path, now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "worker-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return mock, server, worker
}

func findNotification(t *testing.T, mock *Server, eventID string) mockNotification {
	t.Helper()
	var latest mockNotification
	for _, item := range mock.notifications {
		if item.Event.ResourceID == eventID {
			if item.Attempt > latest.Attempt {
				latest = item
			}
		}
	}
	if latest.ID == "" {
		t.Fatalf("notification for event %s not found", eventID)
	}
	return latest
}

func TestNotificationWorkerRetriesAndRecoversAfterMockRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	mock, server, store := notificationWorkerTestServer(t, path, now)
	issueID := testBeforeIssueNotification(t, mock)
	sender := &notificationSenderStub{err: notificationRateLimit{}}
	worker := notificationworker.Worker{ID: "gateway-notification-worker", Store: store, Sender: sender, Now: now}

	first, err := worker.RunOnce(context.Background(), 1)
	if err != nil || first.Claimed != 1 || first.Retried != 1 || first.Dead != 0 || len(first.Failures) != 1 || first.Failures[0].ErrorCode != "MAX_RATE_LIMIT" {
		t.Fatalf("first send = %+v, %v", first, err)
	}
	failed := findNotification(t, mock, issueID)
	if failed.Status != "retry" || failed.Attempt != 1 || failed.ErrorCode == nil || *failed.ErrorCode != "MAX_RATE_LIMIT" || failed.NextAttemptAt == nil || !failed.NextAttemptAt.Equal(clock.Add(5*time.Second)) {
		t.Fatalf("retry was not persisted: %+v", failed)
	}
	if issue, found := mock.issues[issueID]; !found || issue.ID != issueID || mock.notifications[failed.ID].Status != "retry" {
		t.Fatalf("MAX send failure changed committed issue or lost its outbox row: issue=%+v found=%t", issue, found)
	}
	server.Close()

	clock = clock.Add(5 * time.Second)
	restarted, server, store := notificationWorkerTestServer(t, path, now)
	defer server.Close()
	sender.err = nil
	worker = notificationworker.Worker{ID: "gateway-notification-worker", Store: store, Sender: sender, Now: now}
	second, err := worker.RunOnce(context.Background(), 1)
	if err != nil || second.Claimed != 1 || second.Sent != 1 || len(second.Failures) != 0 || len(sender.calls) != 2 {
		t.Fatalf("recovered send = %+v, %v; calls=%d", second, err, len(sender.calls))
	}
	sent := findNotification(t, restarted, issueID)
	if sent.Status != "sent" || sent.Attempt != 2 || sent.ProviderID == nil || !strings.Contains(sender.calls[1], "Создано замечание") {
		t.Fatalf("recovered delivery did not persist: %+v", sent)
	}
}

func TestTripStartRemainsCommittedWhenMAXNotificationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	mock, server, store := notificationWorkerTestServer(t, path, now)
	defer server.Close()
	scenario := scenarioContext{
		t: t, client: commandClient(t, mock), worker: store, mock: mock,
		ctx: context.Background(), now: clock, clock: &clock,
	}
	ready := scenario.readyCheckout()
	started, err := scenario.client.CheckoutStart(context.Background(), driverID, ready.ID, ready.Version, "notification-start-trip", nil)
	if err != nil {
		t.Fatal(err)
	}
	trip, err := dataapi.DecodeAggregate[dataapi.Trip](started)
	if err != nil || trip.Status != "active" {
		t.Fatalf("trip start did not commit: trip=%+v err=%v", trip, err)
	}

	sender := &notificationSenderStub{err: notificationRateLimit{}}
	worker := notificationworker.Worker{ID: "gateway-notification-worker", Store: store, Sender: sender, Now: now}
	result, err := worker.RunOnce(context.Background(), 1)
	if err != nil || result.Retried != 1 || result.Dead != 0 {
		t.Fatalf("notification failure result = %+v, %v", result, err)
	}
	if current, found := mock.trips[trip.ID]; !found || current.Status != "active" || current.EmployeeID != trip.EmployeeID {
		t.Fatalf("MAX send failure rolled back the trip: trip=%+v found=%t", current, found)
	}
	notification := findNotification(t, mock, trip.ID)
	if notification.Event.Type != "trip_started" || notification.Status != "retry" {
		t.Fatalf("trip event was not left durable for retry: %+v", notification)
	}
}

func TestNotificationWorkerCanDuplicateAfterMAXSendBeforeAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	mock, server, store := notificationWorkerTestServer(t, path, now)
	issueID := testBeforeIssueNotification(t, mock)
	sender := &notificationSenderStub{}
	failingAck := &failNotificationAckOnce{Store: store, fail: true}
	worker := notificationworker.Worker{ID: "gateway-notification-worker", Store: failingAck, Sender: sender, Now: now}

	first, err := worker.RunOnce(context.Background(), 1)
	if err == nil || first.Claimed != 1 || first.Sent != 0 || len(first.Failures) != 1 || first.Failures[0].Operation != "ack" || len(sender.calls) != 1 {
		t.Fatalf("lost ack was not surfaced: %+v, %v; sends=%d", first, err, len(sender.calls))
	}
	leased := findNotification(t, mock, issueID)
	if leased.Status != "leased" || leased.Attempt != 1 {
		t.Fatalf("failed ack incorrectly completed the delivery: %+v", leased)
	}
	server.Close()

	// The old lease expires after a process restart. Since MAX may already have
	// accepted the first send, reclaiming it can deliver a duplicate.
	clock = clock.Add(3 * time.Minute)
	restarted, server, store := notificationWorkerTestServer(t, path, now)
	defer server.Close()
	worker = notificationworker.Worker{ID: "gateway-notification-worker", Store: store, Sender: sender, Now: now}
	second, err := worker.RunOnce(context.Background(), 1)
	if err != nil || second.Claimed != 1 || second.Sent != 1 || len(second.Failures) != 0 || len(sender.calls) != 2 {
		t.Fatalf("expired delivery was not recovered: %+v, %v; sends=%d", second, err, len(sender.calls))
	}
	sent := findNotification(t, restarted, issueID)
	if sent.Status != "sent" || sent.Attempt != 2 {
		t.Fatalf("reclaimed delivery state = %+v", sent)
	}
}
