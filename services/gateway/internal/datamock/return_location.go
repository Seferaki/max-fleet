package datamock

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) setReturnLocation(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(command.Payload, &fields)
	var input dataapi.LocationInput
	if !strictPayload(command.Payload, &input) || len(fields) < 4 || len(fields) > 5 || !input.Confirmed ||
		math.IsNaN(input.Latitude) || math.IsInf(input.Latitude, 0) || input.Latitude < -90 || input.Latitude > 90 ||
		math.IsNaN(input.Longitude) || math.IsInf(input.Longitude, 0) || input.Longitude < -180 || input.Longitude > 180 ||
		(input.Source != "max_geo" && input.Source != "manual_map" && input.Source != "admin") || input.Landmark != nil && len(*input.Landmark) > 500 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	for _, name := range []string{"latitude", "longitude", "source", "confirmed"} {
		value, found := fields[name]
		if !found || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return dataapi.CommandResult{}, false
		}
	}
	draft, found := s.returns[command.TargetID]
	trip := s.trips[draft.TripID]
	employee := s.employees[actor]
	if !found || (trip.EmployeeID != employee.ID && employee.Role != "admin") {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if input.Source == "admin" && employee.Role != "admin" || input.Source != "admin" && trip.EmployeeID != employee.ID {
		s.fail(w, requestID, http.StatusForbidden, "ACCESS_DENIED")
		return dataapi.CommandResult{}, false
	}
	if draft.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", draft.Version)
		return dataapi.CommandResult{}, false
	}
	if draft.Status != "draft" || draft.IntentConfirmedAt == nil || trip.Status != "returning" || trip.ReturnID == nil || *trip.ReturnID != draft.ID {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	draft.ParkingLocation = &dataapi.ParkingLocation{ID: newRequestID(), Latitude: input.Latitude, Longitude: input.Longitude, Source: input.Source, Landmark: input.Landmark, ConfirmedAt: now}
	draft.Version++
	draft.UpdatedAt = now
	s.returns[draft.ID] = draft
	return commandResult("return.set_location", draft), true
}
