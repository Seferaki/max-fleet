package datamock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const maxNotificationAttempts = 5

type notificationTransitionInput struct {
	LeaseToken        string     `json:"lease_token"`
	ProviderMessageID string     `json:"provider_message_id,omitempty"`
	ErrorCode         string     `json:"error_code,omitempty"`
	RetryAfter        *time.Time `json:"retry_after,omitempty"`
	Dead              bool       `json:"dead,omitempty"`
}

func (s *Server) ackNotification(w http.ResponseWriter, r *http.Request, requestID string) {
	s.transitionNotification(w, r, requestID, false)
}

func (s *Server) retryNotification(w http.ResponseWriter, r *http.Request, requestID string) {
	s.transitionNotification(w, r, requestID, true)
}

func (s *Server) transitionNotification(w http.ResponseWriter, r *http.Request, requestID string, retry bool) {
	id, key := r.PathValue("id"), r.Header.Get("Idempotency-Key")
	if !validUUID(id) || !validInboxString(key, 8, 200) {
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
	if json.Unmarshal(body, &fields) != nil || (retry && (len(fields) != 4 || fields["lease_token"] == nil || fields["error_code"] == nil || fields["retry_after"] == nil || fields["dead"] == nil)) || (!retry && (len(fields) != 2 || fields["lease_token"] == nil || fields["provider_message_id"] == nil)) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input notificationTransitionInput
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !validInboxString(input.LeaseToken, 1, 200) || retry && (!validInboxString(input.ErrorCode, 1, 80) || string(fields["dead"]) == "null") || !retry && !validInboxString(input.ProviderMessageID, 1, 200) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	canonical, _ := json.Marshal(input)
	hash := sha256.Sum256(canonical)
	signature := hex.EncodeToString(hash[:])
	identityKey := r.URL.Path + "\x00" + key

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveSnapshot == nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	if prior, found := s.notificationTransitions[identityKey]; found {
		if prior.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		s.success(w, requestID, prior.Result)
		return
	}
	item, found := s.notifications[id]
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	now := s.now().UTC()
	if !validNotificationLease(item, input.LeaseToken, now) {
		s.fail(w, requestID, http.StatusConflict, "LEASE_EXPIRED")
		return
	}
	if retry && ((input.Dead && input.RetryAfter != nil) || (!input.Dead && (input.RetryAfter == nil || !input.RetryAfter.After(now)))) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	before := s.snapshot()
	item.LeaseToken = ""
	item.LeaseExpiresAt = nil
	item.WorkerID = ""
	state := "sent"
	if retry {
		errorCode := input.ErrorCode
		item.ErrorCode = &errorCode
		item.ProviderID = nil
		state = "retry"
		item.NextAttemptAt = input.RetryAfter
		if input.Dead || item.Attempt >= maxNotificationAttempts {
			state = "dead"
			item.NextAttemptAt = nil
		}
	} else {
		providerID := input.ProviderMessageID
		item.ProviderID = &providerID
		item.ErrorCode = nil
		item.NextAttemptAt = nil
	}
	item.Status = state
	s.notifications[id] = item
	result := dataapi.QueueTransition{ID: id, State: state, UpdatedAt: now}
	s.notificationTransitions[identityKey] = notificationTransitionRecord{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}
