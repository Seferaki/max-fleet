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

const integrationLeaseDuration = 2 * time.Minute

type mockIntegration struct {
	Data       dataapi.Integration `json:"data"`
	LeaseToken string              `json:"lease_token"`
	WorkerID   string              `json:"worker_id"`
}

type integrationLeaseRecord struct {
	Signature string                   `json:"signature"`
	Result    dataapi.IntegrationLease `json:"result"`
}

type integrationLeaseInput struct {
	WorkerID        string `json:"worker_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request, requestID string) {
	key := r.PathValue("key")
	if !validInboxString(key, 1, 100) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	integration, found := s.integrations[key]
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, integration.Data)
}

func (s *Server) leaseIntegration(w http.ResponseWriter, r *http.Request, requestID string) {
	integrationKey, key := r.PathValue("key"), r.Header.Get("Idempotency-Key")
	if !validInboxString(integrationKey, 1, 100) || !validInboxString(key, 8, 200) {
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
	if json.Unmarshal(body, &fields) != nil || len(fields) != 2 || fields["worker_id"] == nil || fields["expected_version"] == nil {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input integrationLeaseInput
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !validInboxString(input.WorkerID, 1, 100) || input.ExpectedVersion < 1 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	canonical, _ := json.Marshal(input)
	hash := sha256.Sum256(canonical)
	signature := hex.EncodeToString(hash[:])
	recordKey := r.URL.Path + "\x00" + key

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveSnapshot == nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	integration, found := s.integrations[integrationKey]
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	now := s.now().UTC()
	if prior, found := s.integrationLeases[recordKey]; found {
		if prior.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		if integration.LeaseToken != prior.Result.LeaseToken || integration.Data.LeaseExpiresAt == nil || !integration.Data.LeaseExpiresAt.After(now) {
			s.fail(w, requestID, http.StatusConflict, "LEASE_EXPIRED")
			return
		}
		s.success(w, requestID, prior.Result)
		return
	}
	if integration.Data.Version != input.ExpectedVersion {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", integration.Data.Version)
		return
	}
	active := integration.Data.LeaseExpiresAt != nil && integration.Data.LeaseExpiresAt.After(now)
	if active && integration.WorkerID != input.WorkerID {
		s.fail(w, requestID, http.StatusConflict, "COMMAND_IN_PROGRESS")
		return
	}
	before := s.snapshot()
	if !active {
		integration.LeaseToken = newRequestID()
	}
	expires := now.Add(integrationLeaseDuration)
	integration.WorkerID = input.WorkerID
	integration.Data.LeaseExpiresAt = &expires
	integration.Data.Version++
	integration.Data.UpdatedAt = now
	s.integrations[integrationKey] = integration
	result := dataapi.IntegrationLease{LeaseToken: integration.LeaseToken, LeaseExpiresAt: expires, Integration: integration.Data}
	s.integrationLeases[recordKey] = integrationLeaseRecord{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}
