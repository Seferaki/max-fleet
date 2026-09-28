package dataapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// WorkerClient keeps the WorkerBearer separate from the actor-facing client.
type WorkerClient struct {
	transport *Client
}

type WorkerConfig Config

func NewWorker(cfg WorkerConfig) (*WorkerClient, error) {
	transport, err := New(Config(cfg))
	if err != nil {
		return nil, err
	}
	return &WorkerClient{transport: transport}, nil
}

func (c *WorkerClient) StoreInbox(ctx context.Context, event NormalizedEvent, key string) (InboxStored, error) {
	if !validKey(key) || !validMaxID(event.ActorMaxUserID) || !validMaxID(event.ChatID) || event.IntegrationKey == "" || event.EventKey == "" || event.OccurredAt.IsZero() {
		return InboxStored{}, errors.New("data-api: invalid inbox event or idempotency key")
	}
	return workerPost[InboxStored](ctx, c, "/inbox", event, key)
}

func (c *WorkerClient) ClaimInbox(ctx context.Context, workerID string, maxItems int, key string) (InboxClaim, error) {
	if !validKey(key) || workerID == "" || len(workerID) > 100 || strings.ContainsAny(workerID, "\r\n") || maxItems < 1 || maxItems > 50 {
		return InboxClaim{}, errors.New("data-api: invalid inbox claim")
	}
	return workerPost[InboxClaim](ctx, c, "/inbox/claim", struct {
		WorkerID string `json:"worker_id"`
		MaxItems int    `json:"max_items"`
	}{workerID, maxItems}, key)
}

func (c *WorkerClient) AckInbox(ctx context.Context, id, leaseToken, key string) (QueueTransition, error) {
	if !validUUID(id) || !validWorkerLease(leaseToken) || !validKey(key) {
		return QueueTransition{}, errors.New("data-api: invalid inbox ack")
	}
	return workerPost[QueueTransition](ctx, c, "/inbox/"+id+"/ack", struct {
		LeaseToken string `json:"lease_token"`
	}{leaseToken}, key)
}

func (c *WorkerClient) RetryInbox(ctx context.Context, id, leaseToken, errorCode string, nextAttemptAt time.Time, key string) (QueueTransition, error) {
	if !validUUID(id) || !validWorkerLease(leaseToken) || errorCode == "" || len(errorCode) > 80 || strings.ContainsAny(errorCode, "\r\n") || nextAttemptAt.IsZero() || !validKey(key) {
		return QueueTransition{}, errors.New("data-api: invalid inbox retry")
	}
	return workerPost[QueueTransition](ctx, c, "/inbox/"+id+"/retry", struct {
		LeaseToken    string    `json:"lease_token"`
		ErrorCode     string    `json:"error_code"`
		NextAttemptAt time.Time `json:"next_attempt_at"`
	}{leaseToken, errorCode, nextAttemptAt.UTC()}, key)
}

func (c *WorkerClient) ClaimNotifications(ctx context.Context, workerID string, maxItems int, key string) (NotificationClaim, error) {
	if !validWorkerString(workerID, 1, 100) || maxItems < 1 || maxItems > 50 || !validKey(key) {
		return NotificationClaim{}, errors.New("data-api: invalid notification claim")
	}
	return workerPost[NotificationClaim](ctx, c, "/notifications/claim", struct {
		WorkerID string `json:"worker_id"`
		MaxItems int    `json:"max_items"`
	}{workerID, maxItems}, key)
}

func (c *WorkerClient) AckNotification(ctx context.Context, id, leaseToken, providerMessageID, key string) (QueueTransition, error) {
	if !validUUID(id) || !validWorkerLease(leaseToken) || !validWorkerString(providerMessageID, 1, 200) || !validKey(key) {
		return QueueTransition{}, errors.New("data-api: invalid notification ack")
	}
	return workerPost[QueueTransition](ctx, c, "/notifications/"+id+"/ack", struct {
		LeaseToken        string `json:"lease_token"`
		ProviderMessageID string `json:"provider_message_id"`
	}{leaseToken, providerMessageID}, key)
}

func (c *WorkerClient) RetryNotification(ctx context.Context, id, leaseToken, errorCode string, retryAfter *time.Time, dead bool, key string) (QueueTransition, error) {
	if !validUUID(id) || !validWorkerLease(leaseToken) || !validWorkerString(errorCode, 1, 80) || !validKey(key) || dead && retryAfter != nil || !dead && (retryAfter == nil || retryAfter.IsZero()) {
		return QueueTransition{}, errors.New("data-api: invalid notification retry")
	}
	var next *time.Time
	if retryAfter != nil {
		utc := retryAfter.UTC()
		next = &utc
	}
	return workerPost[QueueTransition](ctx, c, "/notifications/"+id+"/retry", struct {
		LeaseToken string     `json:"lease_token"`
		ErrorCode  string     `json:"error_code"`
		RetryAfter *time.Time `json:"retry_after"`
		Dead       bool       `json:"dead"`
	}{leaseToken, errorCode, next, dead}, key)
}

func (c *WorkerClient) GetIntegration(ctx context.Context, integrationKey string) (Integration, error) {
	if !validIntegrationKey(integrationKey) {
		return Integration{}, errors.New("data-api: invalid integration key")
	}
	return requestWithMode[Integration](ctx, c.transport, http.MethodGet, "/integrations/"+integrationKey, "", nil, "", "", nil, nil, true)
}

func (c *WorkerClient) LeaseIntegration(ctx context.Context, integrationKey, workerID string, expectedVersion int64, key string) (IntegrationLease, error) {
	if !validIntegrationKey(integrationKey) || !validWorkerString(workerID, 1, 100) || expectedVersion < 1 || !validKey(key) {
		return IntegrationLease{}, errors.New("data-api: invalid integration lease")
	}
	return workerPost[IntegrationLease](ctx, c, "/integrations/"+integrationKey+"/lease", struct {
		WorkerID        string `json:"worker_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{workerID, expectedVersion}, key)
}

func (c *WorkerClient) CheckpointIntegration(ctx context.Context, integrationKey, leaseToken string, expectedVersion int64, previousMarker *string, newMarker string, storedEventIDs []string, key string) (Integration, error) {
	if !validIntegrationKey(integrationKey) || !validWorkerLease(leaseToken) || expectedVersion < 1 || !validWorkerString(newMarker, 0, 500) || previousMarker != nil && !validWorkerString(*previousMarker, 0, 500) || storedEventIDs == nil || len(storedEventIDs) > 50 || !validKey(key) {
		return Integration{}, errors.New("data-api: invalid integration checkpoint")
	}
	seen := make(map[string]bool, len(storedEventIDs))
	for _, id := range storedEventIDs {
		if !validUUID(id) || seen[id] {
			return Integration{}, errors.New("data-api: invalid stored event ID")
		}
		seen[id] = true
	}
	return workerPost[Integration](ctx, c, "/integrations/"+integrationKey+"/checkpoint", struct {
		LeaseToken      string   `json:"lease_token"`
		ExpectedVersion int64    `json:"expected_version"`
		PreviousMarker  *string  `json:"previous_marker"`
		NewMarker       string   `json:"new_marker"`
		StoredEventIDs  []string `json:"stored_event_ids"`
	}{leaseToken, expectedVersion, previousMarker, newMarker, storedEventIDs}, key)
}

func workerPost[T any](ctx context.Context, c *WorkerClient, path string, value any, key string) (T, error) {
	var zero T
	body, err := json.Marshal(value)
	if err != nil {
		return zero, errors.New("data-api: invalid worker request")
	}
	return requestWithMode[T](ctx, c.transport, http.MethodPost, path, "", body, "application/json", key, nil, nil, true)
}

func validWorkerLease(token string) bool {
	return token != "" && len(token) <= 200 && !strings.ContainsAny(token, "\r\n")
}

func validWorkerString(value string, minLen, maxLen int) bool {
	if len(value) < minLen || len(value) > maxLen {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validIntegrationKey(key string) bool {
	return validWorkerString(key, 1, 100) && !strings.ContainsAny(key, "/\\?#")
}
