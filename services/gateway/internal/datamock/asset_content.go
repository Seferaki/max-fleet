package datamock

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
