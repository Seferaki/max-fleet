package dataapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
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
