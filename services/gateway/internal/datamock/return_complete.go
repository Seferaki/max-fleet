package datamock

import (
	"net/http"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) completeReturn(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload struct {
		Attestation bool `json:"attestation"`
	}
	if !hasFields(command.Payload, "attestation") || !strictPayload(command.Payload, &payload) || !payload.Attestation {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	draft, found := s.returns[command.TargetID]
	trip := s.trips[draft.TripID]
	employee := s.employees[actor]
	if !found || trip.EmployeeID != employee.ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if draft.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", draft.Version)
		return dataapi.CommandResult{}, false
	}
	if draft.Status != "draft" || draft.IntentConfirmedAt == nil || trip.Status != "returning" || trip.ReturnID == nil || *trip.ReturnID != draft.ID || employee.ActiveTripID == nil || *employee.ActiveTripID != trip.ID {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	if len(draft.Inspection.MissingSlots) != 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "PHOTO_SET_INCOMPLETE", "message": "PHOTO_SET_INCOMPLETE", "retryable": false, "details": map[string]any{"missing_slots": draft.Inspection.MissingSlots}}, "request_id": requestID})
		return dataapi.CommandResult{}, false
	}
	inspection := draft.Inspection
	if inspection.Status != "draft" || inspection.Phase != "after" || inspection.PhotosConfirmedAt == nil || inspection.FuelLevel == nil || inspection.OdometerKM == nil || inspection.NewDamage == nil || inspection.CabinClean == nil || inspection.ParkingAllowed == nil || inspection.KeysReturned == nil || inspection.CarLocked == nil {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	if draft.ParkingLocation == nil {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "LOCATION_REQUIRED")
		return dataapi.CommandResult{}, false
	}
	if !*inspection.ParkingAllowed || !*inspection.KeysReturned || !*inspection.CarLocked {
		s.fail(w, requestID, http.StatusConflict, "UNSAFE_RETURN")
		return dataapi.CommandResult{}, false
	}
	if trip.BeforeInspection.OdometerKM != nil && *inspection.OdometerKM < *trip.BeforeInspection.OdometerKM {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "ODOMETER_ROLLBACK")
		return dataapi.CommandResult{}, false
	}
	vehicleIndex := -1
	for i := range s.vehicles {
		if s.vehicles[i].ID == trip.VehicleID {
			vehicleIndex = i
			break
		}
	}
	if vehicleIndex < 0 || s.vehicles[vehicleIndex].Status != "in_trip" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	inspection.Status = "finalized"
	inspection.Version++
	inspection.UpdatedAt = now
	draft.Inspection = inspection
	draft.Status = "completed"
	draft.Step = "completed"
	draft.Version++
	draft.UpdatedAt = now
	s.returns[draft.ID] = draft
	trip.Status = "completed"
	trip.EndedAt = &now
	trip.AfterInspection = &inspection
	trip.ParkingLocation = draft.ParkingLocation
	trip.Version++
	trip.UpdatedAt = now
	s.trips[trip.ID] = trip
	employee.ActiveTripID = nil
	employee.Version++
	employee.UpdatedAt = now
	s.employees[actor] = employee
	vehicle := &s.vehicles[vehicleIndex]
	vehicle.CurrentParking = draft.ParkingLocation
	vehicle.CurrentFuel = inspection.FuelLevel
	vehicle.CurrentOdometerKM = inspection.OdometerKM
	vehicle.FuelConfirmedAt = &now
	vehicle.OdometerConfirmedAt = &now
	if *inspection.NewDamage || !*inspection.CabinClean {
		vehicle.NeedsReview = true
	}
	vehicle.Status = "available"
	if vehicle.ManualBlocked || vehicle.NeedsReview {
		vehicle.Status = "unavailable"
	}
	vehicle.Version++
	vehicle.UpdatedAt = now
	return commandResult("return.complete", draft), true
}
