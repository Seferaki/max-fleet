package datamock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type photoRecord struct {
	AssetID        string `json:"asset_id"`
	SHA256         string `json:"sha256"`
	SourceEventKey string `json:"source_event_key"`
	ContentType    string `json:"content_type"`
}

type photoAttempt struct {
	Signature string                    `json:"signature"`
	Result    dataapi.PhotoUploadResult `json:"result"`
}

func (s *Server) uploadPhoto(w http.ResponseWriter, r *http.Request, requestID string) {
	inspectionID := r.PathValue("id")
	slot, slotErr := strconv.Atoi(r.PathValue("slot"))
	key := r.Header.Get("Idempotency-Key")
	if !validUUID(inspectionID) || slotErr != nil || slot < 1 || slot > 8 || len(key) < 8 || len(key) > 200 || strings.ContainsAny(key, "\r\n") {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if s.assetDir == "" {
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	imageBytes, contentType, version, sourceEventKey, status := readPhotoForm(w, r)
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
	hash := sha256.Sum256(imageBytes)
	sha := hex.EncodeToString(hash[:])
	actor := r.Header.Get("X-Actor-Max-ID")
	identity := actor + ":" + key
	signature := inspectionID + ":" + strconv.Itoa(slot) + ":" + strconv.FormatInt(version, 10) + ":" + sourceEventKey + ":" + sha
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.photoResults[identity]; exists {
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
	var checkout dataapi.Checkout
	var checkoutID string
	var draft dataapi.Return
	var returnID string
	for id, candidate := range s.checkouts {
		if candidate.Inspection.ID == inspectionID {
			checkout, checkoutID = candidate, id
			break
		}
	}
	if checkoutID == "" {
		for id, candidate := range s.returns {
			if candidate.Inspection.ID == inspectionID {
				draft, returnID = candidate, id
				break
			}
		}
	}
	var inspection *dataapi.Inspection
	if checkoutID != "" && checkout.EmployeeID == s.employees[actor].ID {
		inspection = &checkout.Inspection
	} else if returnID != "" && s.trips[draft.TripID].EmployeeID == s.employees[actor].ID {
		inspection = &draft.Inspection
	} else {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if checkoutID != "" && checkout.Status == "expired" {
		s.failVersion(w, requestID, http.StatusConflict, "HOLD_EXPIRED", checkout.Inspection.Version)
		return
	}
	if checkoutID != "" && checkout.Status != "holding" || returnID != "" && (draft.Status != "draft" || draft.IntentConfirmedAt == nil || s.trips[draft.TripID].Status != "returning") || inspection.Status != "draft" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return
	}
	if inspection.Version != version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", inspection.Version)
		return
	}
	for _, existing := range s.photos[inspectionID] {
		if existing.SHA256 == sha || existing.SourceEventKey == sourceEventKey {
			s.fail(w, requestID, http.StatusUnprocessableEntity, "DUPLICATE_PHOTO")
			return
		}
	}
	assetID := newRequestID()
	assetPath := filepath.Join(s.assetDir, assetID)
	if err := atomicAssetWrite(assetPath, imageBytes); err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	before := s.snapshot()
	if s.photos[inspectionID] == nil {
		s.photos[inspectionID] = make(map[int]photoRecord)
	}
	s.photos[inspectionID][slot] = photoRecord{AssetID: assetID, SHA256: sha, SourceEventKey: sourceEventKey, ContentType: contentType}
	inspection.OccupiedSlots = []int{}
	inspection.MissingSlots = []int{}
	for current := 1; current <= 8; current++ {
		if _, found := s.photos[inspectionID][current]; found {
			inspection.OccupiedSlots = append(inspection.OccupiedSlots, current)
		} else {
			inspection.MissingSlots = append(inspection.MissingSlots, current)
		}
	}
	now := s.now().UTC()
	inspection.PhotosConfirmedAt = nil
	inspection.Version++
	inspection.UpdatedAt = now
	if checkoutID != "" {
		checkout.Version++
		checkout.UpdatedAt = now
		s.checkouts[checkoutID] = checkout
	} else {
		draft.Version++
		draft.UpdatedAt = now
		s.returns[returnID] = draft
	}
	result := dataapi.PhotoUploadResult{AssetID: assetID, SHA256: sha, Inspection: *inspection}
	s.photoResults[identity] = photoAttempt{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		_ = os.Remove(assetPath)
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	s.success(w, requestID, result)
}

func readPhotoForm(w http.ResponseWriter, r *http.Request) ([]byte, string, int64, string, int) {
	r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(64<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, "", 0, "", http.StatusBadRequest
	}
	var imageBytes []byte
	var contentType, sourceEventKey string
	var version int64
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", 0, "", photoFormErrorStatus(err)
		}
		name := part.FormName()
		if seen[name] || (name != "image" && name != "expected_version" && name != "source_event_key") {
			return nil, "", 0, "", http.StatusBadRequest
		}
		seen[name] = true
		switch name {
		case "image":
			contentType = part.Header.Get("Content-Type")
			imageBytes, err = io.ReadAll(io.LimitReader(part, (10<<20)+1))
			if err != nil {
				return nil, "", 0, "", photoFormErrorStatus(err)
			}
			if len(imageBytes) > 10<<20 {
				return nil, "", 0, "", http.StatusRequestEntityTooLarge
			}
		case "expected_version":
			value, readErr := io.ReadAll(io.LimitReader(part, 32))
			if readErr != nil {
				return nil, "", 0, "", photoFormErrorStatus(readErr)
			}
			version, err = strconv.ParseInt(string(value), 10, 64)
			if err != nil {
				return nil, "", 0, "", http.StatusBadRequest
			}
		case "source_event_key":
			value, readErr := io.ReadAll(io.LimitReader(part, 201))
			if readErr != nil {
				return nil, "", 0, "", photoFormErrorStatus(readErr)
			}
			sourceEventKey = string(value)
		}
	}
	if !seen["image"] || !seen["expected_version"] || !seen["source_event_key"] || len(imageBytes) == 0 || version < 1 || sourceEventKey == "" || len(sourceEventKey) > 200 || strings.ContainsAny(sourceEventKey, "\r\n") {
		return nil, "", 0, "", http.StatusBadRequest
	}
	formats := map[string]string{"image/jpeg": "jpeg", "image/png": "png", "image/webp": "webp"}
	expectedFormat, allowed := formats[contentType]
	if !allowed {
		return nil, "", 0, "", http.StatusUnsupportedMediaType
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil || format != expectedFormat || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 25_000_000 {
		return nil, "", 0, "", http.StatusUnsupportedMediaType
	}
	if _, format, err = image.Decode(bytes.NewReader(imageBytes)); err != nil || format != expectedFormat {
		return nil, "", 0, "", http.StatusUnsupportedMediaType
	}
	return imageBytes, contentType, version, sourceEventKey, 0
}

func photoFormErrorStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func atomicAssetWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".asset-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
