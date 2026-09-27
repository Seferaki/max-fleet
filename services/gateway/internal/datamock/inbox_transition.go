package datamock

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const maxInboxAttempts = 5

type inboxTransitionInput struct {
	LeaseToken    string    `json:"lease_token"`
	ErrorCode     string    `json:"error_code,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
}

func (s *Server) ackInbox(w http.ResponseWriter, r *http.Request, requestID string) {
	s.transitionInbox(w, r, requestID, false)
}

func (s *Server) retryInbox(w http.ResponseWriter, r *http.Request, requestID string) {
	s.transitionInbox(w, r, requestID, true)
}

func (s *Server) transitionInbox(w http.ResponseWriter, r *http.Request, requestID string, retry bool) {
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
	if json.Unmarshal(body, &fields) != nil || fields["lease_token"] == nil || len(fields) != 1 && !retry || len(fields) != 3 && retry || retry && (fields["error_code"] == nil || fields["next_attempt_at"] == nil) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input inboxTransitionInput
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !validInboxString(input.LeaseToken, 1, 200) || retry && (!validInboxString(input.ErrorCode, 1, 80) || input.NextAttemptAt.IsZero()) {
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
	if prior, found := s.inboxTransitions[identityKey]; found {
		if prior.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		s.success(w, requestID, prior.Result)
		return
	}
	identity, event, found := s.inboxByID(id)
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	now := s.now().UTC()
	if !validLease(event, input.LeaseToken, now) {
		s.fail(w, requestID, http.StatusConflict, "LEASE_EXPIRED")
		return
	}
	before := s.snapshot()
	event.LeaseToken = ""
	event.LeaseExpiresAt = nil
	event.WorkerID = ""
	state := "done"
	if retry {
		state = "retry"
		event.NextAttemptAt = &input.NextAttemptAt
		if event.Attempt >= maxInboxAttempts {
			state = "dead"
			event.NextAttemptAt = nil
		}
	}
	event.Status = state
	s.inbox[identity] = event
	result := dataapi.QueueTransition{ID: id, State: state, UpdatedAt: now}
	s.inboxTransitions[identityKey] = inboxTransitionRecord{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}

// Caller holds s.mu. The ID lookup remains scoped to the private mock state.
func (s *Server) inboxByID(id string) (string, mockInboxEvent, bool) {
	for identity, event := range s.inbox {
		if event.Stored.ID == id {
			return identity, event, true
		}
	}
	return "", mockInboxEvent{}, false
}

func validLease(event mockInboxEvent, token string, now time.Time) bool {
	return event.Status == "leased" && event.LeaseExpiresAt != nil && event.LeaseExpiresAt.After(now) && token != "" && subtle.ConstantTimeCompare([]byte(event.LeaseToken), []byte(token)) == 1
}

// Caller holds s.mu. A stale worker cannot execute a new domain command.
func (s *Server) validCommandLease(eventID, actor, token string, now time.Time) bool {
	_, event, found := s.inboxByID(eventID)
	return found && event.Event.ActorMaxUserID == actor && validLease(event, token, now)
}
