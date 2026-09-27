package datamock

import (
	"net/http"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) startCheckout(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload struct {
		Attestation bool `json:"attestation"`
	}
	if !hasFields(command.Payload, "attestation") || !strictPayload(command.Payload, &payload) || !payload.Attestation {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	checkout, found := s.checkouts[command.TargetID]
	employee := s.employees[actor]
	if !found || checkout.EmployeeID != employee.ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if checkout.Status == "expired" {
		s.failVersion(w, requestID, http.StatusConflict, "HOLD_EXPIRED", checkout.Version)
		return dataapi.CommandResult{}, false
	}
	if checkout.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", checkout.Version)
		return dataapi.CommandResult{}, false
	}
	if checkout.Status != "holding" || checkout.Inspection.Status != "draft" || employee.ActiveTripID != nil {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	if !employee.CanStartTrip {
		s.fail(w, requestID, http.StatusForbidden, "CANNOT_START_TRIP")
		return dataapi.CommandResult{}, false
	}
	if checkout.IntentConfirmedAt == nil || checkout.RulesAcceptedAt == nil || checkout.RulesVersionID == nil || *checkout.RulesVersionID != s.rules.ID {
		s.fail(w, requestID, http.StatusConflict, "RULES_REQUIRED")
		return dataapi.CommandResult{}, false
	}
	if len(checkout.Inspection.MissingSlots) != 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "PHOTO_SET_INCOMPLETE", "message": "PHOTO_SET_INCOMPLETE", "retryable": false, "details": map[string]any{"missing_slots": checkout.Inspection.MissingSlots}}, "request_id": requestID})
		return dataapi.CommandResult{}, false
	}
	if checkout.Inspection.PhotosConfirmedAt == nil || checkout.Inspection.FuelLevel == nil || checkout.Inspection.OdometerKM == nil || checkout.NoNewIssues == nil || !*checkout.NoNewIssues || checkout.Inspection.NewDamage != nil && *checkout.Inspection.NewDamage {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	vehicleIndex := -1
	for i := range s.vehicles {
		if s.vehicles[i].ID == checkout.VehicleID {
			vehicleIndex = i
			break
		}
	}
	if vehicleIndex < 0 || s.vehicles[vehicleIndex].Status != "holding" || s.vehicles[vehicleIndex].ManualBlocked || s.vehicles[vehicleIndex].NeedsReview {
		s.fail(w, requestID, http.StatusConflict, "VEHICLE_UNAVAILABLE")
		return dataapi.CommandResult{}, false
	}
	if s.vehicles[vehicleIndex].CurrentOdometerKM != nil && *checkout.Inspection.OdometerKM < *s.vehicles[vehicleIndex].CurrentOdometerKM {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "ODOMETER_ROLLBACK")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	tripID := newRequestID()
	checkout.Status = "started"
	checkout.Step = "active_trip"
	checkout.Version++
	checkout.UpdatedAt = now
	checkout.Inspection.Status = "finalized"
	checkout.Inspection.Version++
	checkout.Inspection.UpdatedAt = now
	s.checkouts[checkout.ID] = checkout
	trip := dataapi.Trip{ID: tripID, VehicleID: checkout.VehicleID, EmployeeID: employee.ID, CheckoutID: checkout.ID, Status: "active", StartedAt: now, MissingData: []string{}, BeforeInspection: checkout.Inspection, Issues: []dataapi.Issue{}, Version: 1, UpdatedAt: now}
	s.trips[tripID] = trip
	employee.ActiveTripID = &tripID
	employee.Version++
	employee.UpdatedAt = now
	s.employees[actor] = employee
	vehicle := &s.vehicles[vehicleIndex]
	vehicle.Status = "in_trip"
	vehicle.CurrentFuel = checkout.Inspection.FuelLevel
	vehicle.CurrentOdometerKM = checkout.Inspection.OdometerKM
	vehicle.FuelConfirmedAt = &now
	vehicle.OdometerConfirmedAt = &now
	vehicle.Version++
	vehicle.UpdatedAt = now
	return commandResult("checkout.start", trip), true
}
