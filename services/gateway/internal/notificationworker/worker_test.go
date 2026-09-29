package notificationworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type memoryDelivery struct {
	item           dataapi.NotificationLease
	state          string
	retryAfter     *time.Time
	leaseExpiresAt time.Time
}

type memoryStore struct {
	now        func() time.Time
	deliveries []*memoryDelivery
	retryCodes []string
	ackKeys    []string
}

func (s *memoryStore) ClaimNotifications(_ context.Context, _ string, maxItems int, _ string) (dataapi.NotificationClaim, error) {
	now := s.now().UTC()
	claimed := dataapi.NotificationClaim{Items: []dataapi.NotificationLease{}}
	blocked := make(map[string]bool)
	for _, delivery := range s.deliveries {
		if delivery.state == "sent" || delivery.state == "dead" || blocked[delivery.item.RecipientMaxUserID] {
			continue
		}
		blocked[delivery.item.RecipientMaxUserID] = true
		if delivery.state == "leased" && delivery.leaseExpiresAt.After(now) || delivery.retryAfter != nil && delivery.retryAfter.After(now) {
			continue
		}
		delivery.state = "leased"
		delivery.item.Attempt++
		delivery.item.LeaseToken = fmt.Sprintf("lease-token-%d", delivery.item.Attempt)
		delivery.leaseExpiresAt = now.Add(2 * time.Minute)
		delivery.item.LeaseExpiresAt = delivery.leaseExpiresAt
		delivery.retryAfter = nil
		claimed.Items = append(claimed.Items, delivery.item)
		if len(claimed.Items) >= maxItems {
			break
		}
	}
	return claimed, nil
}

func (s *memoryStore) AckNotification(_ context.Context, id, leaseToken, providerMessageID, key string) (dataapi.QueueTransition, error) {
	delivery := s.find(id)
	if delivery == nil || delivery.state != "leased" || delivery.item.LeaseToken != leaseToken || providerMessageID == "" {
		return dataapi.QueueTransition{}, errors.New("synthetic stale lease")
	}
	delivery.state = "sent"
	s.ackKeys = append(s.ackKeys, key)
	return dataapi.QueueTransition{ID: id, State: "sent"}, nil
}

func (s *memoryStore) RetryNotification(_ context.Context, id, leaseToken, code string, retryAfter *time.Time, dead bool, _ string) (dataapi.QueueTransition, error) {
	delivery := s.find(id)
	if delivery == nil || delivery.state != "leased" || delivery.item.LeaseToken != leaseToken {
		return dataapi.QueueTransition{}, errors.New("synthetic stale lease")
	}
	s.retryCodes = append(s.retryCodes, code)
	if dead {
		delivery.state = "dead"
	} else {
		if retryAfter == nil {
			return dataapi.QueueTransition{}, errors.New("missing retry deadline")
		}
		delivery.state = "retry"
		delivery.retryAfter = retryAfter
	}
	return dataapi.QueueTransition{ID: id, State: delivery.state}, nil
}

func (s *memoryStore) find(id string) *memoryDelivery {
	for _, delivery := range s.deliveries {
		if delivery.item.DeliveryID == id {
			return delivery
		}
	}
	return nil
}

type sendResult struct {
	userID int64
	text   string
}

type senderStub struct {
	calls []sendResult
	err   error
}

func (s *senderStub) SendText(_ context.Context, userID int64, text string) (string, error) {
	s.calls = append(s.calls, sendResult{userID: userID, text: text})
	if s.err != nil {
		return "", s.err
	}
	return fmt.Sprintf("max-message-%d", len(s.calls)), nil
}

type codedSendError struct{ code string }

func (e *codedSendError) Error() string                 { return e.code }
func (e *codedSendError) NotificationErrorCode() string { return e.code }

func testWorker(now *time.Time, store Store, sender Sender) Worker {
	return Worker{ID: "notification-test-worker", Store: store, Sender: sender, Now: func() time.Time { return *now }}
}

func testStore(now *time.Time, recipient string) *memoryStore {
	return &memoryStore{
		now: func() time.Time { return *now },
		deliveries: []*memoryDelivery{{state: "pending", item: dataapi.NotificationLease{
			DeliveryID: "7cbd36f0-e23f-46b6-9b2f-0d10b4b6dc5b",
			Event: dataapi.NotificationEvent{
				Type:       "trip_admin_closed",
				ResourceID: "84212591-fdaf-41aa-8f27-e4c4ba7d7561",
				OccurredAt: time.Date(2026, 9, 29, 9, 30, 0, 0, time.UTC),
			},
			RecipientMaxUserID: recipient,
		}}},
	}
}

func TestWorkerSendsOutboxEventAndAcknowledgesProviderReceipt(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := testStore(&now, "8000000000000000001")
	sender := &senderStub{}
	worker := testWorker(&now, store, sender)
	result, err := worker.RunOnce(context.Background(), 10)
	if err != nil || result != (Result{Claimed: 1, Sent: 1}) {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if len(sender.calls) != 1 || sender.calls[0].userID != 8000000000000000001 || !strings.Contains(sender.calls[0].text, "Поездка закрыта администратором") || strings.Contains(sender.calls[0].text, "actor") {
		t.Fatalf("unexpected MAX send: %+v", sender.calls)
	}
	delivery := store.deliveries[0]
	if delivery.state != "sent" || len(store.ackKeys) != 1 || !strings.HasPrefix(store.ackKeys[0], "notification-ack:") {
		t.Fatalf("delivery was not acknowledged: state=%s keys=%v", delivery.state, store.ackKeys)
	}
	if transitionKey(delivery.item, "ack") != store.ackKeys[0] {
		t.Fatal("ack idempotency key was not stable for the lease")
	}
}

func TestWorkerRetriesRateLimitWithBackoffAndResumesAfterRestart(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := testStore(&now, "8000000000000000001")
	blockedSender := &senderStub{err: &codedSendError{code: "MAX_RATE_LIMIT"}}
	firstWorker := testWorker(&now, store, blockedSender)
	first, err := firstWorker.RunOnce(context.Background(), 10)
	wantRetryAt := now.Add(5 * time.Second)
	if err != nil || first != (Result{Claimed: 1, Retried: 1}) || store.deliveries[0].state != "retry" || store.retryCodes[0] != "MAX_RATE_LIMIT" || store.deliveries[0].retryAfter == nil || !store.deliveries[0].retryAfter.Equal(wantRetryAt) {
		t.Fatalf("rate-limit retry = %+v, %v; delivery=%+v", first, err, store.deliveries[0])
	}

	now = wantRetryAt
	restartedSender := &senderStub{}
	restartedWorker := testWorker(&now, store, restartedSender)
	recovered, err := restartedWorker.RunOnce(context.Background(), 10)
	if err != nil || recovered != (Result{Claimed: 1, Sent: 1}) || len(restartedSender.calls) != 1 || store.deliveries[0].state != "sent" {
		t.Fatalf("recovery = %+v, %v; sends=%d state=%s", recovered, err, len(restartedSender.calls), store.deliveries[0].state)
	}
}

func TestWorkerDocumentsAtLeastOnceWhenMAXResponseIsLost(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := testStore(&now, "8000000000000000001")
	// The fake provider accepted the first send but its response was lost.
	uncertainSender := &senderStub{err: &codedSendError{code: "MAX_SEND_FAILED"}}
	firstWorker := testWorker(&now, store, uncertainSender)
	first, err := firstWorker.RunOnce(context.Background(), 1)
	if err != nil || first != (Result{Claimed: 1, Retried: 1}) {
		t.Fatalf("uncertain send = %+v, %v", first, err)
	}
	now = now.Add(backoff(1))
	restartedSender := &senderStub{}
	secondWorker := testWorker(&now, store, restartedSender)
	second, err := secondWorker.RunOnce(context.Background(), 1)
	if err != nil || second != (Result{Claimed: 1, Sent: 1}) || len(uncertainSender.calls)+len(restartedSender.calls) != 2 {
		t.Fatalf("retry after uncertain result = %+v, %v; send calls=%d", second, err, len(uncertainSender.calls)+len(restartedSender.calls))
	}
}

func TestWorkerDeadLettersInvalidRecipientAndUnsupportedEvent(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := testStore(&now, "not-a-max-id")
	sender := &senderStub{}
	result, err := testWorker(&now, store, sender).RunOnce(context.Background(), 10)
	if err != nil || result != (Result{Claimed: 1, Dead: 1}) || store.retryCodes[0] != "INVALID_RECIPIENT" || len(sender.calls) != 0 {
		t.Fatalf("invalid recipient result = %+v, %v; codes=%v sends=%d", result, err, store.retryCodes, len(sender.calls))
	}

	message, err := formatMessage(dataapi.NotificationEvent{Type: "unknown", ResourceID: "84212591-fdaf-41aa-8f27-e4c4ba7d7561", OccurredAt: now})
	if err == nil || message != "" {
		t.Fatalf("unsupported event was formatted: %q, %v", message, err)
	}
}

func TestFormatMessageCompactsUntrustedReason(t *testing.T) {
	reason := "Проверить\r\n\t" + strings.Repeat("а", maxReasonRunes+10)
	message, err := formatMessage(dataapi.NotificationEvent{
		Type:       "vehicle_blocked",
		ResourceID: "84212591-fdaf-41aa-8f27-e4c4ba7d7561",
		OccurredAt: time.Date(2026, 9, 29, 9, 30, 0, 0, time.UTC),
		Reason:     &reason,
	})
	if err != nil || strings.Contains(message, "\r") || strings.Contains(message, "\t") || strings.Contains(message, "\n\t") || !strings.Contains(message, "Причина: Проверить "+strings.Repeat("а", maxReasonRunes-9)+"…") {
		t.Fatalf("reason was not compacted safely: %q, %v", message, err)
	}
}

func TestBackoffCapsAtFiveMinutes(t *testing.T) {
	if backoff(1) != 5*time.Second || backoff(5) != 80*time.Second || backoff(7) != 5*time.Minute || backoff(10) != 5*time.Minute {
		t.Fatalf("unexpected backoff: %v %v %v %v", backoff(1), backoff(5), backoff(7), backoff(10))
	}
}
