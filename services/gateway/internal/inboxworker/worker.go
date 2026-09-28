package inboxworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type Store interface {
	ClaimInbox(context.Context, string, int, string) (dataapi.InboxClaim, error)
	AckInbox(context.Context, string, string, string) (dataapi.QueueTransition, error)
	RetryInbox(context.Context, string, string, string, time.Time, string) (dataapi.QueueTransition, error)
}

type Processor interface {
	Handle(context.Context, dataapi.InboxClaimItem) error
}

type ProcessError struct {
	Code string
	Err  error
}

func (e *ProcessError) Error() string { return e.Code }
func (e *ProcessError) Unwrap() error { return e.Err }

type Worker struct {
	ID        string
	Store     Store
	Processor Processor
	Now       func() time.Time
}

type Result struct {
	Claimed int
	Acked   int
	Retried int
	Dead    int
}

var safeCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)

// RunOnce processes a claimed batch in order. The DataAPI owns actor ordering,
// lease fencing and the five-attempt dead-letter transition.
func (w Worker) RunOnce(ctx context.Context, maxItems int) (Result, error) {
	if w.ID == "" || len(w.ID) > 100 || w.Store == nil || w.Processor == nil || w.Now == nil || maxItems < 1 || maxItems > 50 {
		return Result{}, errors.New("inbox worker is not configured")
	}
	claimKey, err := randomKey("inbox-claim:")
	if err != nil {
		return Result{}, err
	}
	claim, err := w.Store.ClaimInbox(ctx, w.ID, maxItems, claimKey)
	if err != nil {
		return Result{}, err
	}
	result := Result{Claimed: len(claim.Items)}
	for _, item := range claim.Items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		processErr := w.Processor.Handle(ctx, item)
		key, err := randomKey("inbox-transition:")
		if err != nil {
			return result, err
		}
		if processErr == nil {
			if _, err := w.Store.AckInbox(ctx, item.ID, item.LeaseToken, key); err != nil {
				return result, err
			}
			result.Acked++
			continue
		}
		code := errorCode(processErr)
		next := w.Now().UTC().Add(backoff(item.Attempt))
		transition, err := w.Store.RetryInbox(ctx, item.ID, item.LeaseToken, code, next, key)
		if err != nil {
			return result, err
		}
		if transition.State == "dead" {
			result.Dead++
		} else {
			result.Retried++
		}
	}
	return result, nil
}

func errorCode(err error) string {
	var processing *ProcessError
	if errors.As(err, &processing) && safeCode.MatchString(processing.Code) {
		return processing.Code
	}
	return "PROCESSING_FAILED"
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	delay := time.Duration(1<<uint(attempt-1)) * 5 * time.Second
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func randomKey(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("cannot generate inbox key")
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
