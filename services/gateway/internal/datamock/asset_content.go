package datamock

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) assetContent(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	actor := r.Header.Get("X-Actor-Max-ID")
	s.mu.Lock()
	defer s.mu.Unlock()
	employee := s.employees[actor]
	var contentType, hash string
	var allowed bool
	if asset, found := s.issueAssets[id]; found {
		allowed = asset.Actor == actor || employee.Role == "admin"
		contentType, hash = asset.ContentType, asset.SHA256
	} else {
		for inspectionID, slots := range s.photos {
			for _, photo := range slots {
				if photo.AssetID != id {
					continue
				}
				var ownerID string
				for _, checkout := range s.checkouts {
					if checkout.Inspection.ID == inspectionID {
						ownerID = checkout.EmployeeID
						break
					}
				}
				if ownerID == "" {
					for _, draft := range s.returns {
						if draft.Inspection.ID == inspectionID {
							ownerID = s.trips[draft.TripID].EmployeeID
							break
						}
					}
				}
				allowed = ownerID != "" && (ownerID == employee.ID || employee.Role == "admin")
				contentType, hash = photo.ContentType, photo.SHA256
				break
			}
			if hash != "" {
				break
			}
		}
	}
	if !allowed || hash == "" {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.writePhotoAsset(w, requestID, id, contentType, hash)
}

func (s *Server) tripInspectionPhoto(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	phase := r.PathValue("phase")
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if !validUUID(id) || (phase != "before" && phase != "after") || err != nil || slot < 1 || slot > 8 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	trip, found := s.trips[id]
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	if !found || (trip.EmployeeID != employee.ID && employee.Role != "admin") {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	var inspection dataapi.Inspection
	if phase == "before" && trip.BeforeInspection.Phase == "before" && trip.BeforeInspection.Status == "finalized" {
		inspection = trip.BeforeInspection
	} else if phase == "after" && (trip.Status == "completed" || trip.Status == "closed_by_admin") && trip.AfterInspection != nil && trip.AfterInspection.Phase == "after" && trip.AfterInspection.Status == "finalized" {
		inspection = *trip.AfterInspection
	}
	photo, found := s.photos[inspection.ID][slot]
	if inspection.ID == "" || !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.writePhotoAsset(w, requestID, photo.AssetID, photo.ContentType, photo.SHA256)
}

// Caller holds s.mu while resolving authorization and the asset record.
func (s *Server) writePhotoAsset(w http.ResponseWriter, requestID, id, contentType, hash string) {
	data, err := os.ReadFile(filepath.Join(s.assetDir, id))
	if err != nil || len(data) == 0 || len(data) > 10<<20 || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
		s.fail(w, requestID, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
