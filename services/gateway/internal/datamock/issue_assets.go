package datamock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type stagedIssueAsset struct {
	ID              string    `json:"id"`
	Actor           string    `json:"actor"`
	ScopeType       string    `json:"scope_type"`
	ScopeID         string    `json:"scope_id"`
	SourceEventKey  string    `json:"source_event_key"`
	SHA256          string    `json:"sha256"`
	ContentType     string    `json:"content_type"`
	ExpiresAt       time.Time `json:"expires_at"`
	AttachedIssueID *string   `json:"attached_issue_id,omitempty"`
}

type stageAttempt struct {
	Signature string              `json:"signature"`
	Result    dataapi.StagedAsset `json:"result"`
}

type stageForm struct {
	Image                                           []byte
	ContentType, ScopeType, ScopeID, SourceEventKey string
}

func readStageForm(w http.ResponseWriter, r *http.Request) (stageForm, int) {
	r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(64<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		return stageForm{}, http.StatusBadRequest
	}
	var form stageForm
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stageForm{}, photoFormErrorStatus(err)
		}
		name := part.FormName()
		if seen[name] || (name != "image" && name != "purpose" && name != "scope_type" && name != "scope_id" && name != "source_event_key") {
			return stageForm{}, http.StatusBadRequest
		}
		seen[name] = true
		limit := int64(201)
		if name == "image" {
			limit = (10 << 20) + 1
			form.ContentType = part.Header.Get("Content-Type")
		}
		value, err := io.ReadAll(io.LimitReader(part, limit))
		if err != nil {
			return stageForm{}, photoFormErrorStatus(err)
		}
		switch name {
		case "image":
			form.Image = value
		case "purpose":
			if string(value) != "issue" {
				return stageForm{}, http.StatusBadRequest
			}
		case "scope_type":
			form.ScopeType = string(value)
		case "scope_id":
			form.ScopeID = string(value)
		case "source_event_key":
			form.SourceEventKey = string(value)
		}
	}
	if len(seen) != 5 || !validUUID(form.ScopeID) || (form.ScopeType != "vehicle" && form.ScopeType != "trip" && form.ScopeType != "inspection") || form.SourceEventKey == "" || len(form.SourceEventKey) > 200 || strings.ContainsAny(form.SourceEventKey, "\r\n") || len(form.Image) == 0 {
		return stageForm{}, http.StatusBadRequest
	}
	if len(form.Image) > 10<<20 {
		return stageForm{}, http.StatusRequestEntityTooLarge
	}
	formats := map[string]string{"image/jpeg": "jpeg", "image/png": "png", "image/webp": "webp"}
	expected, ok := formats[form.ContentType]
	if !ok {
		return stageForm{}, http.StatusUnsupportedMediaType
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(form.Image))
	if err != nil || format != expected || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 25_000_000 {
		return stageForm{}, http.StatusUnsupportedMediaType
	}
	if _, format, err = image.Decode(bytes.NewReader(form.Image)); err != nil || format != expected {
		return stageForm{}, http.StatusUnsupportedMediaType
	}
	return form, 0
}

// stageScopeOwned requires a live issue context owned by the submitting actor.
func (s *Server) stageScopeOwned(actor, scopeType, scopeID string) bool {
	employee := s.employees[actor]
	switch scopeType {
	case "inspection":
		for _, checkout := range s.checkouts {
			if checkout.Inspection.ID == scopeID && checkout.EmployeeID == employee.ID && checkout.Status == "holding" && checkout.Inspection.Status == "draft" {
				return true
			}
		}
		for _, draft := range s.returns {
			trip := s.trips[draft.TripID]
			if draft.Inspection.ID == scopeID && trip.EmployeeID == employee.ID && trip.Status == "returning" && draft.Status == "draft" && draft.Inspection.Status == "draft" && draft.IntentConfirmedAt != nil {
				return true
			}
		}
	case "trip":
		trip := s.trips[scopeID]
		return trip.ID != "" && trip.EmployeeID == employee.ID && (trip.Status == "active" || trip.Status == "returning")
	case "vehicle":
		for _, checkout := range s.checkouts {
			if checkout.VehicleID == scopeID && checkout.EmployeeID == employee.ID && checkout.Status == "holding" {
				return true
			}
		}
		for _, trip := range s.trips {
			if trip.VehicleID == scopeID && trip.EmployeeID == employee.ID && (trip.Status == "active" || trip.Status == "returning") {
				return true
			}
		}
	}
	return false
}

func (s *Server) stageIssueAsset(w http.ResponseWriter, r *http.Request, requestID string) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 200 || strings.ContainsAny(key, "\r\n") {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if s.assetDir == "" {
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	form, status := readStageForm(w, r)
	if status != 0 {
		code := "INVALID_REQUEST"
		if status == http.StatusRequestEntityTooLarge {
			code = "FILE_TOO_LARGE"
		}
		if status == http.StatusUnsupportedMediaType {
			code = "UNSUPPORTED_MEDIA"
		}
		s.fail(w, requestID, status, code)
		return
	}
	sha := sha256.Sum256(form.Image)
	hash := hex.EncodeToString(sha[:])
	actor := r.Header.Get("X-Actor-Max-ID")
	identity := actor + ":" + key
	signature := form.ScopeType + ":" + form.ScopeID + ":" + form.SourceEventKey + ":" + form.ContentType + ":" + hash
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.stageResults[identity]; exists {
		if previous.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
		} else {
			s.success(w, requestID, previous.Result)
		}
		return
	}
	if !s.expireAndSave(w, requestID) {
		return
	}
	if !s.stageScopeOwned(actor, form.ScopeType, form.ScopeID) {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	for _, existing := range s.issueAssets {
		if existing.Actor == actor && existing.ScopeType == form.ScopeType && existing.ScopeID == form.ScopeID && (existing.SHA256 == hash || existing.SourceEventKey == form.SourceEventKey) {
			s.fail(w, requestID, http.StatusUnprocessableEntity, "DUPLICATE_PHOTO")
			return
		}
	}
	id := newRequestID()
	path := filepath.Join(s.assetDir, id)
	if err := atomicAssetWrite(path, form.Image); err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	before := s.snapshot()
	expires := s.now().UTC().Add(30 * time.Minute)
	result := dataapi.StagedAsset{AssetID: id, ExpiresAt: expires}
	s.issueAssets[id] = stagedIssueAsset{ID: id, Actor: actor, ScopeType: form.ScopeType, ScopeID: form.ScopeID, SourceEventKey: form.SourceEventKey, SHA256: hash, ContentType: form.ContentType, ExpiresAt: expires}
	s.stageResults[identity] = stageAttempt{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		_ = os.Remove(path)
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}
