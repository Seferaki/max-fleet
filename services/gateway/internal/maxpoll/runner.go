package maxpoll

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type Store interface {
	GetIntegration(context.Context, string) (dataapi.Integration, error)
	LeaseIntegration(context.Context, string, string, int64, string) (dataapi.IntegrationLease, error)
	StoreInbox(context.Context, dataapi.NormalizedEvent, string) (dataapi.InboxStored, error)
	CheckpointIntegration(context.Context, string, string, int64, *string, string, []string, string) (dataapi.Integration, error)
}

type Runner struct {
	IntegrationKey string
	WorkerID       string
	Source         maxsdk.UpdateSource
	Store          Store
}

type Result struct {
	Stored  int
	Ignored int
	Marker  *string
}

// RunOnce leases one polling integration and advances its marker only after
// every supported event in the batch has a durable inbox ID.
func (r Runner) RunOnce(ctx context.Context) (Result, error) {
	if r.IntegrationKey == "" || r.WorkerID == "" || r.Source == nil || r.Store == nil {
		return Result{}, errors.New("poller is not configured")
	}
	integration, err := r.Store.GetIntegration(ctx, r.IntegrationKey)
	if err != nil {
		return Result{}, err
	}
	if integration.Mode != "polling" {
		return Result{}, errors.New("poller integration is not in polling mode")
	}
	leaseKey, err := randomKey("poll-lease:")
	if err != nil {
		return Result{}, err
	}
	lease, err := r.Store.LeaseIntegration(ctx, r.IntegrationKey, r.WorkerID, integration.Version, leaseKey)
	if err != nil {
		return Result{}, err
	}
	currentMarker, err := parseMarker(lease.Integration.Marker)
	if err != nil {
		return Result{}, err
	}
	updates, nextMarker, err := r.Source.GetUpdates(ctx, currentMarker)
	if err != nil {
		return Result{}, err
	}
	if len(updates) > 50 || nextMarker < currentMarker || nextMarker == currentMarker && len(updates) > 0 {
		return Result{}, errors.New("MAX polling returned an invalid batch or marker")
	}
	storedIDs := make([]string, 0, len(updates))
	result := Result{Marker: lease.Integration.Marker}
	for _, update := range updates {
		event, err := maxsdk.Normalize(r.IntegrationKey, update)
		if errors.Is(err, maxsdk.ErrUnsupportedUpdate) || errors.Is(err, maxsdk.ErrGroupEvent) {
			result.Ignored++
			continue
		}
		if err != nil {
			return result, fmt.Errorf("MAX update cannot be normalized: %w", err)
		}
		stored, err := r.Store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event))
		if err != nil {
			return result, err
		}
		storedIDs = append(storedIDs, stored.ID)
		result.Stored++
	}
	if nextMarker == currentMarker {
		return result, nil
	}
	checkpointKey, err := randomKey("poll-checkpoint:")
	if err != nil {
		return result, err
	}
	marker := strconv.FormatInt(nextMarker, 10)
	checkpoint, err := r.Store.CheckpointIntegration(ctx, r.IntegrationKey, lease.LeaseToken, lease.Integration.Version, lease.Integration.Marker, marker, storedIDs, checkpointKey)
	if err != nil {
		return result, err
	}
	return Result{Stored: result.Stored, Ignored: result.Ignored, Marker: checkpoint.Marker}, nil
}

func parseMarker(marker *string) (int64, error) {
	if marker == nil {
		return 0, nil
	}
	value, err := strconv.ParseInt(*marker, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid polling marker")
	}
	return value, nil
}

func randomKey(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", errors.New("cannot generate polling key")
	}
	return prefix + hex.EncodeToString(bytes[:]), nil
}
