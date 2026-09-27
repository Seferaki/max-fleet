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
	var draft dataapi.Return
	var returnID string
	for id, candidate := range s.checkouts {
		if candidate.Inspection.ID == command.TargetID {
			checkout, checkoutID = candidate, id
			break
		}
	}
	if checkoutID == "" {
		for id, candidate := range s.returns {
			if candidate.Inspection.ID == command.TargetID {
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
		return dataapi.CommandResult{}, false
	}
	if inspection.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", inspection.Version)
		return dataapi.CommandResult{}, false
	}
	if checkoutID != "" && (checkout.Status != "holding" || inspection.Phase != "before" || checkout.RulesAcceptedAt == nil || input.ParkingAllowed != nil || input.KeysReturned != nil || input.CarLocked != nil) || returnID != "" && (draft.Status != "draft" || draft.IntentConfirmedAt == nil || inspection.Phase != "after" || s.trips[draft.TripID].Status != "returning") || inspection.Status != "draft" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	baseline := (*int64)(nil)
	if returnID != "" {
		baseline = s.trips[draft.TripID].BeforeInspection.OdometerKM
	} else {
		for _, vehicle := range s.vehicles {
			if vehicle.ID == checkout.VehicleID {
				baseline = vehicle.CurrentOdometerKM
				break
			}
		}
	}
	if input.OdometerKM != nil && baseline != nil && *input.OdometerKM < *baseline {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "ODOMETER_ROLLBACK")
		return dataapi.CommandResult{}, false
	}
	if input.FuelLevel != nil {
		inspection.FuelLevel = input.FuelLevel
	}
	if input.OdometerKM != nil {
		inspection.OdometerKM = input.OdometerKM
	}
	if input.NewDamage != nil {
		inspection.NewDamage = input.NewDamage
		if checkoutID != "" && *input.NewDamage {
			checkout.NoNewIssues = nil
		}
	}
	if input.CabinClean != nil {
		inspection.CabinClean = input.CabinClean
	}
	if input.ParkingAllowed != nil {
		inspection.ParkingAllowed = input.ParkingAllowed
	}
	if input.KeysReturned != nil {
		inspection.KeysReturned = input.KeysReturned
	}
	if input.CarLocked != nil {
		inspection.CarLocked = input.CarLocked
	}
	now := s.now().UTC()
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
	return commandResult("inspection.update", *inspection), true
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
