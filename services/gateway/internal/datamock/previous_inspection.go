package datamock

import (
	"net/http"
	"strconv"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) previousInspection(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, found := s.latestPreviousInspectionLocked(id)
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, latest)
}

func (s *Server) previousInspectionPhoto(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if !validUUID(id) || err != nil || slot < 1 || slot > 8 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, found := s.latestPreviousInspectionLocked(id)
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	photo, found := s.photos[latest.ID][slot]
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.writePhotoAsset(w, requestID, photo.AssetID, photo.ContentType, photo.SHA256)
}

func (s *Server) latestPreviousInspectionLocked(vehicleID string) (dataapi.Inspection, bool) {
	vehicleExists := false
	for _, vehicle := range s.vehicles {
		if vehicle.ID == vehicleID {
			vehicleExists = true
			break
		}
	}
	if !vehicleExists {
		return dataapi.Inspection{}, false
	}
	var latest dataapi.Inspection
	for _, draft := range s.returns {
		trip := s.trips[draft.TripID]
		if trip.VehicleID != vehicleID || draft.Status != "completed" || draft.Inspection.Status != "finalized" || draft.Inspection.Phase != "after" {
			continue
		}
		if latest.ID == "" || draft.Inspection.UpdatedAt.After(latest.UpdatedAt) {
			latest = draft.Inspection
		}
	}
	if latest.ID == "" {
		return dataapi.Inspection{}, false
	}
	return latest, true
}
