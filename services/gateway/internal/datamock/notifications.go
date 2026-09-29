package datamock

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const notificationLeaseDuration = 2 * time.Minute

type mockNotification struct {
	ID             string                    `json:"id"`
	Event          dataapi.NotificationEvent `json:"event"`
	EnqueuedAt     time.Time                 `json:"enqueued_at"`
	Recipient      string                    `json:"recipient"`
	Status         string                    `json:"status"`
	LeaseToken     string                    `json:"lease_token"`
	LeaseExpiresAt *time.Time                `json:"lease_expires_at"`
	WorkerID       string                    `json:"worker_id"`
	Attempt        int                       `json:"attempt"`
	NextAttemptAt  *time.Time                `json:"next_attempt_at"`
	ProviderID     *string                   `json:"provider_message_id"`
	ErrorCode      *string                   `json:"error_code"`
	Sequence       int64                     `json:"sequence"`
}

type notificationClaimRecord struct {
	Signature string                    `json:"signature"`
	Result    dataapi.NotificationClaim `json:"result"`
}

type notificationTransitionRecord struct {
	Signature string                  `json:"signature"`
	Result    dataapi.QueueTransition `json:"result"`
}

// Called under the domain command mutex, before its snapshot commit.
func (s *Server) enqueueAdminNotification(eventType, resourceID, vehicleID string, at time.Time) {
	vehicle := vehicleID
	s.enqueueNotification(dataapi.NotificationEvent{Type: eventType, ResourceID: resourceID, VehicleID: &vehicle, OccurredAt: at})
}

// Adds one delivery per administrator and any event-specific recipients.
// The caller holds the domain command mutex so these rows commit atomically with the event.
func (s *Server) enqueueNotification(event dataapi.NotificationEvent, additionalRecipients ...string) {
	recipients := make(map[string]struct{})
	for maxID, employee := range s.employees {
		if employee.Role == "admin" {
			recipients[maxID] = struct{}{}
		}
	}
	for _, maxID := range additionalRecipients {
		if validMaxID(maxID) {
			recipients[maxID] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(recipients))
	for recipient := range recipients {
		ordered = append(ordered, recipient)
	}
	sort.Strings(ordered)
	enqueuedAt := s.now().UTC()
	for _, recipient := range ordered {
		id := newRequestID()
		s.notificationSequence++
		s.notifications[id] = mockNotification{ID: id, Event: event, EnqueuedAt: enqueuedAt, Recipient: recipient, Status: "pending", Sequence: s.notificationSequence}
	}
}

func (s *Server) claimNotifications(w http.ResponseWriter, r *http.Request, requestID string) {
	key := r.Header.Get("Idempotency-Key")
	if !validInboxString(key, 8, 200) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 2 || fields["worker_id"] == nil || fields["max_items"] == nil {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input inboxClaimRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !validInboxString(input.WorkerID, 1, 100) || input.MaxItems < 1 || input.MaxItems > 50 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	canonical, _ := json.Marshal(input)
	hash := sha256.Sum256(canonical)
	signature := hex.EncodeToString(hash[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveSnapshot == nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	now := s.now().UTC()
	if prior, found := s.notificationClaims[key]; found {
		if prior.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		for _, lease := range prior.Result.Items {
			current, found := s.notifications[lease.DeliveryID]
			if !found || !validNotificationLease(current, lease.LeaseToken, now) {
				s.fail(w, requestID, http.StatusConflict, "LEASE_EXPIRED")
				return
			}
		}
		s.success(w, requestID, prior.Result)
		return
	}
	before := s.snapshot()
	ids := make([]string, 0, len(s.notifications))
	for id := range s.notifications {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.notifications[ids[i]].Sequence < s.notifications[ids[j]].Sequence
	})
	blocked := make(map[string]bool)
	result := dataapi.NotificationClaim{Items: []dataapi.NotificationLease{}}
	for _, id := range ids {
		item := s.notifications[id]
		if item.EnqueuedAt.IsZero() {
			// Older mock snapshots predate enqueued_at; event time is the only
			// durable lower-bound fallback available for those rows.
			item.EnqueuedAt = item.Event.OccurredAt.UTC()
		}
		if item.Status == "sent" || item.Status == "dead" {
			continue
		}
		if blocked[item.Recipient] {
			continue
		}
		blocked[item.Recipient] = true
		if item.Status == "leased" && item.LeaseExpiresAt != nil && item.LeaseExpiresAt.After(now) || item.NextAttemptAt != nil && item.NextAttemptAt.After(now) {
			continue
		}
		item.Status = "leased"
		item.LeaseToken = newRequestID()
		expires := now.Add(notificationLeaseDuration)
		item.LeaseExpiresAt = &expires
		item.WorkerID = input.WorkerID
		item.Attempt++
		item.NextAttemptAt = nil
		s.notifications[id] = item
		result.Items = append(result.Items, dataapi.NotificationLease{DeliveryID: id, Event: item.Event, RecipientMaxUserID: item.Recipient, EnqueuedAt: item.EnqueuedAt, LeaseToken: item.LeaseToken, LeaseExpiresAt: expires, Attempt: item.Attempt})
		if len(result.Items) >= input.MaxItems {
			break
		}
	}
	s.notificationClaims[key] = notificationClaimRecord{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}

func validNotificationLease(item mockNotification, token string, now time.Time) bool {
	return item.Status == "leased" && item.LeaseExpiresAt != nil && item.LeaseExpiresAt.After(now) && token != "" && subtle.ConstantTimeCompare([]byte(item.LeaseToken), []byte(token)) == 1
}
