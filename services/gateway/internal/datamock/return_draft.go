package datamock

import (
	"net/http"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) beginReturn(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	trip, found := s.trips[command.TargetID]
	if !found || trip.EmployeeID != s.employees[actor].ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if trip.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", trip.Version)
		return dataapi.CommandResult{}, false
	}
	if trip.Status != "active" || trip.ReturnID != nil {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	returnID := newRequestID()
	draft := dataapi.Return{ID: returnID, TripID: trip.ID, Status: "draft", Step: "math", Version: 1, UpdatedAt: now,
		Inspection: dataapi.Inspection{ID: newRequestID(), Phase: "after", Status: "draft", OccupiedSlots: []int{}, MissingSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, Version: 1, UpdatedAt: now}}
	s.returns[returnID] = draft
	trip.Status = "returning"
	trip.ReturnID = &returnID
	trip.Version++
	trip.UpdatedAt = now
	s.trips[trip.ID] = trip
	return commandResult("trip.begin_return", draft), true
}

func (s *Server) cancelReturn(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	draft, found := s.returns[command.TargetID]
	trip := s.trips[draft.TripID]
	if !found || trip.EmployeeID != s.employees[actor].ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if draft.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", draft.Version)
		return dataapi.CommandResult{}, false
	}
	if draft.Status != "draft" || trip.Status != "returning" || trip.ReturnID == nil || *trip.ReturnID != draft.ID {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	draft.Status = "cancelled"
	draft.Step = "cancelled"
	draft.Version++
	draft.UpdatedAt = now
	draft.Inspection.Status = "abandoned"
	draft.Inspection.Version++
	draft.Inspection.UpdatedAt = now
	s.returns[draft.ID] = draft
	trip.Status = "active"
	trip.ReturnID = nil
	trip.Version++
	trip.UpdatedAt = now
	s.trips[trip.ID] = trip
	return commandResult("return.cancel", draft), true
}
