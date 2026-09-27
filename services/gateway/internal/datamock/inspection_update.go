package datamock

import (
	"encoding/json"
	"net/http"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) updateInspection(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(command.Payload, &fields) != nil || len(fields) == 0 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	for _, value := range fields {
		if string(value) == "null" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return dataapi.CommandResult{}, false
		}
	}
	var input dataapi.InspectionUpdateInput
	if !strictPayload(command.Payload, &input) || input.FuelLevel == nil && input.OdometerKM == nil && input.NewDamage == nil && input.CabinClean == nil && input.ParkingAllowed == nil && input.KeysReturned == nil && input.CarLocked == nil ||
		input.FuelLevel != nil && !validFuelLevel(*input.FuelLevel) || input.OdometerKM != nil && *input.OdometerKM < 0 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	var checkout dataapi.Checkout
	var checkoutID string
	for id, candidate := range s.checkouts {
		if candidate.Inspection.ID == command.TargetID {
			checkout, checkoutID = candidate, id
			break
		}
	}
	if checkoutID == "" || checkout.EmployeeID != s.employees[actor].ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if checkout.Inspection.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", checkout.Inspection.Version)
		return dataapi.CommandResult{}, false
	}
	if checkout.Status != "holding" || checkout.Inspection.Phase != "before" || checkout.Inspection.Status != "draft" || checkout.RulesAcceptedAt == nil || input.ParkingAllowed != nil || input.KeysReturned != nil || input.CarLocked != nil {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	for _, vehicle := range s.vehicles {
		if vehicle.ID == checkout.VehicleID && input.OdometerKM != nil && vehicle.CurrentOdometerKM != nil && *input.OdometerKM < *vehicle.CurrentOdometerKM {
			s.fail(w, requestID, http.StatusUnprocessableEntity, "ODOMETER_ROLLBACK")
			return dataapi.CommandResult{}, false
		}
	}
	if input.FuelLevel != nil {
		checkout.Inspection.FuelLevel = input.FuelLevel
	}
	if input.OdometerKM != nil {
		checkout.Inspection.OdometerKM = input.OdometerKM
	}
	if input.NewDamage != nil {
		checkout.Inspection.NewDamage = input.NewDamage
		if *input.NewDamage {
			checkout.NoNewIssues = nil
		}
	}
	if input.CabinClean != nil {
		checkout.Inspection.CabinClean = input.CabinClean
	}
	now := s.now().UTC()
	checkout.Inspection.Version++
	checkout.Inspection.UpdatedAt = now
	checkout.Version++
	checkout.UpdatedAt = now
	s.checkouts[checkoutID] = checkout
	return commandResult("inspection.update", checkout.Inspection), true
}

func (s *Server) setNoNewIssues(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload struct {
		Value bool `json:"value"`
	}
	if !hasFields(command.Payload, "value") || !strictPayload(command.Payload, &payload) || !payload.Value {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	checkout, found := s.checkouts[command.TargetID]
	if !found || checkout.EmployeeID != s.employees[actor].ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if checkout.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", checkout.Version)
		return dataapi.CommandResult{}, false
	}
	if checkout.Status != "holding" || checkout.RulesAcceptedAt == nil || checkout.Inspection.NewDamage != nil && *checkout.Inspection.NewDamage {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	value := true
	checkout.NoNewIssues = &value
	checkout.Version++
	checkout.UpdatedAt = now
	s.checkouts[checkout.ID] = checkout
	return commandResult("checkout.set_no_new_issues", checkout), true
}

func validFuelLevel(level int) bool {
	return level == 0 || level == 25 || level == 50 || level == 75 || level == 100
}
