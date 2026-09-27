package datamock

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type mockInboxEvent struct {
	Event          dataapi.NormalizedEvent `json:"event"`
	Stored         dataapi.InboxStored     `json:"stored"`
	Signature      string                  `json:"signature"`
	Status         string                  `json:"status"`
	LeaseToken     string                  `json:"lease_token"`
	LeaseExpiresAt *time.Time              `json:"lease_expires_at"`
	WorkerID       string                  `json:"worker_id"`
	Attempt        int                     `json:"attempt"`
	NextAttemptAt  *time.Time              `json:"next_attempt_at"`
	Sequence       int64                   `json:"sequence"`
}

type inboxKeyRecord struct {
	EventIdentity string `json:"event_identity"`
	Signature     string `json:"signature"`
}

type inboxClaimRecord struct {
	Signature string             `json:"signature"`
	Result    dataapi.InboxClaim `json:"result"`
}

const inboxLeaseDuration = 2 * time.Minute

func (s *Server) authorizeWorker(next route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validUUID(requestID) {
			requestID = newRequestID()
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.workerToken == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(given), []byte(s.workerToken)) != 1 {
			s.fail(w, requestID, http.StatusUnauthorized, "INVALID_SERVICE_TOKEN")
			return
		}
		if r.Header.Get("X-Contract-Version") != dataapi.ContractVersion || r.Header.Get("X-Actor-Max-ID") != "" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		next(w, r, requestID)
	}
}

func (s *Server) storeInbox(w http.ResponseWriter, r *http.Request, requestID string) {
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.fail(w, requestID, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE")
		} else {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		}
		return
	}
	event, ok := parseNormalizedEvent(body)
	if !ok {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	canonical, _ := json.Marshal(event)
	signature := sha256.Sum256(canonical)
	sig := hex.EncodeToString(signature[:])
	identity := event.IntegrationKey + "\x00" + event.EventKey

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveSnapshot == nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	if prior, found := s.inboxKeys[key]; found {
		if prior.Signature != sig || prior.EventIdentity != identity {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
	}
	stored, duplicate := s.inbox[identity]
	if duplicate && stored.Signature != sig {
		s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
		return
	}
	if duplicate {
		if _, known := s.inboxKeys[key]; known {
			result := stored.Stored
			result.Duplicate = true
			s.success(w, requestID, result)
			return
		}
	}
	before := s.snapshot()
	if !duplicate {
		s.inboxSequence++
		stored = mockInboxEvent{Event: event, Stored: dataapi.InboxStored{ID: newRequestID(), StoredAt: s.now().UTC()}, Signature: sig, Status: "pending", Sequence: s.inboxSequence}
	}
	s.inbox[identity] = stored
	s.inboxKeys[key] = inboxKeyRecord{EventIdentity: identity, Signature: sig}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	result := stored.Stored
	result.Duplicate = duplicate
	s.success(w, requestID, result)
}

type inboxClaimRequest struct {
	WorkerID string `json:"worker_id"`
	MaxItems int    `json:"max_items"`
}

func (s *Server) claimInbox(w http.ResponseWriter, r *http.Request, requestID string) {
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
	if prior, found := s.inboxClaims[key]; found {
		if prior.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		for _, lease := range prior.Result.Items {
			current, found := s.inbox[lease.Event.IntegrationKey+"\x00"+lease.Event.EventKey]
			if !found || current.Stored.ID != lease.ID || current.LeaseToken != lease.LeaseToken || current.LeaseExpiresAt == nil || !current.LeaseExpiresAt.After(now) || current.Status != "leased" {
				s.fail(w, requestID, http.StatusConflict, "LEASE_EXPIRED")
				return
			}
		}
		s.success(w, requestID, prior.Result)
		return
	}
	before := s.snapshot()
	identities := make([]string, 0, len(s.inbox))
	for identity := range s.inbox {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		left, right := s.inbox[identities[i]], s.inbox[identities[j]]
		return left.Sequence < right.Sequence
	})
	result := dataapi.InboxClaim{Items: []dataapi.InboxClaimItem{}}
	seenActor := make(map[string]bool)
	for _, identity := range identities {
		if len(result.Items) >= input.MaxItems {
			break
		}
		event := s.inbox[identity]
		if event.Status == "done" || event.Status == "dead" || seenActor[event.Event.ActorMaxUserID] {
			continue
		}
		seenActor[event.Event.ActorMaxUserID] = true
		if event.Status == "leased" && event.LeaseExpiresAt != nil && event.LeaseExpiresAt.After(now) || event.NextAttemptAt != nil && event.NextAttemptAt.After(now) {
			continue
		}
		token := newRequestID()
		expires := now.Add(inboxLeaseDuration)
		event.Status = "leased"
		event.LeaseToken = token
		event.LeaseExpiresAt = &expires
		event.WorkerID = input.WorkerID
		event.Attempt++
		event.NextAttemptAt = nil
		s.inbox[identity] = event
		result.Items = append(result.Items, dataapi.InboxClaimItem{ID: event.Stored.ID, Event: event.Event, LeaseToken: token, LeaseExpiresAt: expires, Attempt: event.Attempt})
	}
	s.inboxClaims[key] = inboxClaimRecord{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}

func parseNormalizedEvent(body []byte) (dataapi.NormalizedEvent, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 9 {
		return dataapi.NormalizedEvent{}, false
	}
	for _, name := range []string{"integration_key", "event_key", "event_type", "actor_max_user_id", "chat_id", "message_id", "callback_id", "occurred_at", "payload"} {
		if _, exists := fields[name]; !exists {
			return dataapi.NormalizedEvent{}, false
		}
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(fields["payload"], &payload) != nil || len(payload) != 7 {
		return dataapi.NormalizedEvent{}, false
	}
	for _, name := range []string{"kind", "text", "callback_data", "photo_source_key", "latitude", "longitude", "attachment_count"} {
		if _, exists := payload[name]; !exists {
			return dataapi.NormalizedEvent{}, false
		}
	}
	var event dataapi.NormalizedEvent
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF {
		return dataapi.NormalizedEvent{}, false
	}
	if !validInboxString(event.IntegrationKey, 1, 100) || !validInboxString(event.EventKey, 1, 200) || !validMaxID(event.ActorMaxUserID) || !validMaxID(event.ChatID) || event.OccurredAt.IsZero() || event.Payload.AttachmentCount < 0 || event.Payload.AttachmentCount > 100 {
		return dataapi.NormalizedEvent{}, false
	}
	if event.MessageID != nil && !validInboxString(*event.MessageID, 1, 200) || event.CallbackID != nil && !validInboxString(*event.CallbackID, 1, 200) || event.Payload.Text != nil && len(*event.Payload.Text) > 1000 || event.Payload.CallbackData != nil && len(*event.Payload.CallbackData) > 200 || event.Payload.PhotoSourceKey != nil && len(*event.Payload.PhotoSourceKey) > 500 {
		return dataapi.NormalizedEvent{}, false
	}
	switch event.EventType {
	case "bot_started":
		return event, event.Payload.Kind == "start" && event.MessageID == nil && event.CallbackID == nil
	case "message_created":
		if event.MessageID == nil || event.CallbackID != nil || event.EventKey != "message:"+*event.MessageID+":message_created" {
			return dataapi.NormalizedEvent{}, false
		}
		switch event.Payload.Kind {
		case "text":
			return event, event.Payload.Text != nil && event.Payload.AttachmentCount == 0
		case "photo":
			return event, event.Payload.PhotoSourceKey != nil && event.Payload.AttachmentCount == 1
		case "geo":
			return event, event.Payload.Latitude != nil && event.Payload.Longitude != nil && *event.Payload.Latitude >= -90 && *event.Payload.Latitude <= 90 && *event.Payload.Longitude >= -180 && *event.Payload.Longitude <= 180
		}
	case "message_callback":
		return event, event.CallbackID != nil && event.MessageID == nil && event.EventKey == "callback:"+*event.CallbackID+":message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil
	}
	return dataapi.NormalizedEvent{}, false
}

func validInboxString(value string, minLen, maxLen int) bool {
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
