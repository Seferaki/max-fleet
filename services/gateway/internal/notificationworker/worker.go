package notificationworker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const (
	defaultAttempts = 5
	maxReasonRunes  = 240
)

type Store interface {
	ClaimNotifications(context.Context, string, int, string) (dataapi.NotificationClaim, error)
	AckNotification(context.Context, string, string, string, string) (dataapi.QueueTransition, error)
	RetryNotification(context.Context, string, string, string, *time.Time, bool, string) (dataapi.QueueTransition, error)
}

type Sender interface {
	SendText(context.Context, int64, string) (string, error)
}

type Worker struct {
	ID     string
	Store  Store
	Sender Sender
	Now    func() time.Time
}

type Result struct {
	Claimed int
	Sent    int
	Retried int
	Dead    int
}

type safeSendError interface {
	NotificationErrorCode() string
}

var safeCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)

// Run claims immediately, then polls at interval. observe receives only counts
// and safe errors; notification payloads and recipient IDs are never logged here.
func (w Worker) Run(ctx context.Context, interval time.Duration, maxItems int, observe func(Result, error)) error {
	if interval <= 0 || interval > time.Hour {
		return errors.New("invalid notification worker interval")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		result, err := w.RunOnce(ctx, maxItems)
		if ctx.Err() != nil {
			return nil
		}
		if observe != nil {
			observe(result, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w Worker) RunOnce(ctx context.Context, maxItems int) (Result, error) {
	if !validWorkerString(w.ID, 1, 100) || w.Store == nil || w.Sender == nil || w.Now == nil || maxItems < 1 || maxItems > 50 {
		return Result{}, errors.New("notification worker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	claimKey, err := randomKey("notification-claim:")
	if err != nil {
		return Result{}, err
	}
	claim, err := w.Store.ClaimNotifications(ctx, w.ID, maxItems, claimKey)
	if err != nil {
		return Result{}, err
	}
	result := Result{Claimed: len(claim.Items)}
	for _, item := range claim.Items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !validLease(item, w.Now().UTC()) {
			return result, errors.New("notification worker received an invalid lease")
		}
		message, eventErr := formatMessage(item.Event)
		userID, userErr := parseRecipient(item.RecipientMaxUserID)
		if eventErr != nil || userErr != nil {
			code := "UNSUPPORTED_NOTIFICATION"
			if userErr != nil {
				code = "INVALID_RECIPIENT"
			}
			transition, err := w.Store.RetryNotification(ctx, item.DeliveryID, item.LeaseToken, code, nil, true, transitionKey(item, "dead"))
			if err != nil {
				return result, err
			}
			if transition.State != "dead" {
				return result, errors.New("notification worker could not dead-letter an invalid delivery")
			}
			result.Dead++
			continue
		}

		providerMessageID, sendErr := w.Sender.SendText(ctx, userID, message)
		if ctx.Err() != nil {
			// MAX may have accepted the send; leave the lease to expire and rely on
			// at-least-once delivery rather than inventing a success receipt.
			return result, ctx.Err()
		}
		if sendErr != nil || !validWorkerString(providerMessageID, 1, 200) {
			code := sendErrorCode(sendErr)
			dead := item.Attempt >= defaultAttempts
			var retryAfter *time.Time
			if !dead {
				next := w.Now().UTC().Add(backoff(item.Attempt))
				retryAfter = &next
			}
			transition, err := w.Store.RetryNotification(ctx, item.DeliveryID, item.LeaseToken, code, retryAfter, dead, transitionKey(item, "retry"))
			if err != nil {
				return result, err
			}
			if dead {
				if transition.State != "dead" {
					return result, errors.New("notification worker could not dead-letter an exhausted delivery")
				}
				result.Dead++
			} else {
				if transition.State != "retry" {
					return result, errors.New("notification worker could not schedule a retry")
				}
				result.Retried++
			}
			continue
		}

		if err := ctx.Err(); err != nil {
			// Sending succeeded, but ack cannot be trusted once shutdown cancels the
			// request. A later lease can resend; duplicates are possible by design.
			return result, err
		}
		transition, err := w.Store.AckNotification(ctx, item.DeliveryID, item.LeaseToken, providerMessageID, transitionKey(item, "ack"))
		if err != nil {
			return result, err
		}
		if transition.State != "sent" {
			return result, errors.New("notification worker did not receive a sent acknowledgement")
		}
		result.Sent++
	}
	return result, nil
}

func validLease(item dataapi.NotificationLease, now time.Time) bool {
	return validUUID(item.DeliveryID) && !item.EnqueuedAt.IsZero() && item.LeaseToken != "" && len(item.LeaseToken) <= 200 && !strings.ContainsAny(item.LeaseToken, "\r\n") && item.Attempt > 0 && item.LeaseExpiresAt.After(now)
}

func parseRecipient(value string) (int64, error) {
	userID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || userID <= 0 || strconv.FormatInt(userID, 10) != value {
		return 0, errors.New("invalid notification recipient")
	}
	return userID, nil
}

func formatMessage(event dataapi.NotificationEvent) (string, error) {
	titles := map[string]string{
		"trip_started":      "Началась поездка",
		"trip_completed":    "Поездка завершена",
		"trip_admin_closed": "Поездка закрыта администратором",
		"issue_created":     "Создано замечание",
		"issue_resolved":    "Замечание закрыто",
		"vehicle_blocked":   "Автомобиль заблокирован",
		"access_changed":    "Изменён доступ сотрудника",
	}
	title, ok := titles[event.Type]
	if !ok || !validUUID(event.ResourceID) || event.OccurredAt.IsZero() {
		return "", errors.New("unsupported notification event")
	}
	if event.VehicleID != nil && !validUUID(*event.VehicleID) {
		return "", errors.New("invalid notification vehicle")
	}
	var message strings.Builder
	message.WriteString("MAX Fleet: ")
	message.WriteString(title)
	message.WriteString("\nЗапись: ")
	message.WriteString(event.ResourceID)
	if event.VehicleID != nil {
		message.WriteString("\nАвтомобиль: ")
		message.WriteString(*event.VehicleID)
	}
	message.WriteString("\nВремя: ")
	message.WriteString(event.OccurredAt.UTC().Format("2006-01-02 15:04 UTC"))
	if reason := compactReason(event.Type, event.Reason); reason != "" {
		message.WriteString("\nПричина: ")
		message.WriteString(reason)
	}
	return message.String(), nil
}

func compactReason(eventType string, reason *string) string {
	if reason == nil {
		return ""
	}
	if eventType == "access_changed" && *reason == "granted" {
		return "доступ выдан"
	}
	var out strings.Builder
	spaces := false
	runes := 0
	for _, char := range strings.TrimSpace(*reason) {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			spaces = out.Len() > 0
			continue
		}
		if spaces {
			out.WriteByte(' ')
			spaces = false
		}
		if runes == maxReasonRunes {
			out.WriteString("…")
			break
		}
		out.WriteRune(char)
		runes++
	}
	return strings.TrimSpace(out.String())
}

func sendErrorCode(err error) string {
	var classified safeSendError
	if err != nil && errors.As(err, &classified) {
		if code := classified.NotificationErrorCode(); safeCode.MatchString(code) && strings.HasPrefix(code, "MAX_") {
			return code
		}
	}
	return "MAX_SEND_FAILED"
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

func transitionKey(item dataapi.NotificationLease, action string) string {
	hash := sha256.Sum256([]byte(item.DeliveryID + "\x00" + item.LeaseToken + "\x00" + action))
	return fmt.Sprintf("notification-%s:%s", action, hex.EncodeToString(hash[:]))
}

func randomKey(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("cannot generate notification claim key")
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func validWorkerString(value string, minLength, maxLength int) bool {
	if len(value) < minLength || len(value) > maxLength {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
		} else if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}
