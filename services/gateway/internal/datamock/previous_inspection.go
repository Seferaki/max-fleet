package datamock

import (
	"net/http"

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
	vehicleExists := false
	for _, vehicle := range s.vehicles {
		if vehicle.ID == id {
			vehicleExists = true
			break
		}
	}
	if !vehicleExists {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	var latest dataapi.Inspection
	for _, draft := range s.returns {
		trip := s.trips[draft.TripID]
		if trip.VehicleID != id || draft.Status != "completed" || draft.Inspection.Status != "finalized" || draft.Inspection.Phase != "after" {
			continue
		}
		if latest.ID == "" || draft.Inspection.UpdatedAt.After(latest.UpdatedAt) {
			latest = draft.Inspection
		}
	}
	if latest.ID == "" {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, latest)
}
