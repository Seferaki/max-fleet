package datamock

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type vehicleBlockPayload struct {
	Reason      string `json:"reason"`
	ChallengeID string `json:"challenge_id"`
}

type vehicleUnblockPayload struct {
	Reason          string `json:"reason"`
	ReviewCompleted bool   `json:"review_completed"`
	ChallengeID     string `json:"challenge_id"`
}

type employeeGrantPayload struct {
	MaxUserID   string `json:"max_user_id"`
	DisplayName string `json:"display_name"`
	ChallengeID string `json:"challenge_id"`
}

type employeeAccessPayload struct {
	CanStartTrip bool   `json:"can_start_trip"`
	Reason       string `json:"reason"`
	ChallengeID  string `json:"challenge_id"`
}

type adminClosePayload struct {
	Reason        string                  `json:"reason"`
	ChallengeID   string                  `json:"challenge_id"`
	AvailableData *dataapi.AdminCloseData `json:"available_data,omitempty"`
}

func (s *Server) adminCommand(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	switch command.Operation {
	case "vehicle.block":
		return s.vehicleBlock(w, requestID, actor, command)
	case "vehicle.unblock":
		return s.vehicleUnblock(w, requestID, actor, command)
	case "employee.grant":
		return s.employeeGrant(w, requestID, actor, command)
	case "employee.access":
		return s.employeeAccess(w, requestID, actor, command)
	case "trip.admin_close":
		return s.tripAdminClose(w, requestID, actor, command)
	default:
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
}

func (s *Server) requireAdmin(w http.ResponseWriter, requestID, actor string) bool {
	if s.employees[actor].Role != "admin" {
		s.fail(w, requestID, http.StatusForbidden, "ADMIN_REQUIRED")
		return false
	}
	return true
}

func (s *Server) vehicleBlock(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload vehicleBlockPayload
	if !hasFields(command.Payload, "reason", "challenge_id") || !strictPayload(command.Payload, &payload) || !validAdminReason(payload.Reason) || !validUUID(payload.ChallengeID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.requireAdmin(w, requestID, actor) {
		return dataapi.CommandResult{}, false
	}
	index := s.vehicleIndex(command.TargetID)
	if index < 0 {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	vehicle := &s.vehicles[index]
	if vehicle.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", vehicle.Version)
		return dataapi.CommandResult{}, false
	}
	reason := payload.Reason
	version := command.Version
	intent := adminChallengeIntent{Operation: "vehicle.block", TargetID: &command.TargetID, ExpectedVersion: &version, Reason: &reason}
	proof, ok := s.validateAdminProof(w, requestID, actor, payload.ChallengeID, "vehicle_block", intent)
	if !ok {
		return dataapi.CommandResult{}, false
	}
	if vehicle.ManualBlocked {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	s.consumeAdminProof(proof, now)
	s.cancelHoldForVehicle(vehicle.ID, now, "vehicle_blocked")
	vehicle.ManualBlocked = true
	if vehicle.Status != "in_trip" {
		vehicle.Status = "unavailable"
	}
	vehicle.Version++
	vehicle.UpdatedAt = now
	return commandResult("vehicle.block", *vehicle), true
}

func (s *Server) vehicleUnblock(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload vehicleUnblockPayload
	if !hasFields(command.Payload, "reason", "review_completed", "challenge_id") || !strictPayload(command.Payload, &payload) || !validAdminReason(payload.Reason) || !payload.ReviewCompleted || !validUUID(payload.ChallengeID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.requireAdmin(w, requestID, actor) {
		return dataapi.CommandResult{}, false
	}
	index := s.vehicleIndex(command.TargetID)
	if index < 0 {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	vehicle := &s.vehicles[index]
	if vehicle.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", vehicle.Version)
		return dataapi.CommandResult{}, false
	}
	reason := payload.Reason
	reviewCompleted := payload.ReviewCompleted
	version := command.Version
	intent := adminChallengeIntent{Operation: "vehicle.unblock", TargetID: &command.TargetID, ExpectedVersion: &version, Reason: &reason, ReviewCompleted: &reviewCompleted}
	proof, ok := s.validateAdminProof(w, requestID, actor, payload.ChallengeID, "vehicle_unblock", intent)
	if !ok {
		return dataapi.CommandResult{}, false
	}
	if !vehicle.ManualBlocked && !vehicle.NeedsReview || s.hasBlockingIssue(vehicle.ID) {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	s.consumeAdminProof(proof, now)
	vehicle.ManualBlocked = false
	vehicle.NeedsReview = false
	vehicle.Status = "available"
	vehicle.Version++
	vehicle.UpdatedAt = now
	return commandResult("vehicle.unblock", *vehicle), true
}

func (s *Server) employeeGrant(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload employeeGrantPayload
	if !command.NullTarget || !command.NullVersion || !hasFields(command.Payload, "max_user_id", "display_name", "challenge_id") || !strictPayload(command.Payload, &payload) || !validMaxID(payload.MaxUserID) || !validAdminName(payload.DisplayName) || !validUUID(payload.ChallengeID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.requireAdmin(w, requestID, actor) {
		return dataapi.CommandResult{}, false
	}
	maxUserID, displayName := payload.MaxUserID, payload.DisplayName
	intent := adminChallengeIntent{Operation: "employee.grant", MaxUserID: &maxUserID, DisplayName: &displayName}
	proof, ok := s.validateAdminProof(w, requestID, actor, payload.ChallengeID, "employee_grant", intent)
	if !ok {
		return dataapi.CommandResult{}, false
	}
	if _, exists := s.employees[payload.MaxUserID]; exists {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	s.consumeAdminProof(proof, now)
	employee := dataapi.Employee{ID: newRequestID(), MaxUserID: payload.MaxUserID, DisplayName: payload.DisplayName, Role: "employee", CanStartTrip: true, Version: 1, UpdatedAt: now}
	s.employees[payload.MaxUserID] = employee
	return commandResult("employee.grant", employee), true
}

func (s *Server) employeeAccess(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload employeeAccessPayload
	if !hasFields(command.Payload, "can_start_trip", "reason", "challenge_id") || !strictPayload(command.Payload, &payload) || !validAdminReason(payload.Reason) || !validUUID(payload.ChallengeID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.requireAdmin(w, requestID, actor) {
		return dataapi.CommandResult{}, false
	}
	maxID, employee, found := s.employeeByID(command.TargetID)
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if employee.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", employee.Version)
		return dataapi.CommandResult{}, false
	}
	canStart, reason := payload.CanStartTrip, payload.Reason
	version := command.Version
	intent := adminChallengeIntent{Operation: "employee.access", TargetID: &command.TargetID, ExpectedVersion: &version, CanStartTrip: &canStart, Reason: &reason}
	proof, ok := s.validateAdminProof(w, requestID, actor, payload.ChallengeID, "employee_access", intent)
	if !ok {
		return dataapi.CommandResult{}, false
	}
	if employee.CanStartTrip == payload.CanStartTrip {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	s.consumeAdminProof(proof, now)
	if !payload.CanStartTrip {
		s.cancelHoldForEmployee(employee.ID, now, "employee_blocked")
	}
	employee.CanStartTrip = payload.CanStartTrip
	employee.Version++
	employee.UpdatedAt = now
	s.employees[maxID] = employee
	return commandResult("employee.access", employee), true
}

func (s *Server) tripAdminClose(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload adminClosePayload
	if !validAdminClosePayloadShape(command.Payload) || !strictPayload(command.Payload, &payload) || !validAdminReason(payload.Reason) || !validUUID(payload.ChallengeID) || !validMockAdminCloseData(payload.AvailableData) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.requireAdmin(w, requestID, actor) {
		return dataapi.CommandResult{}, false
	}
	trip, found := s.trips[command.TargetID]
	if !found {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if trip.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", trip.Version)
		return dataapi.CommandResult{}, false
	}
	reason := payload.Reason
	version := command.Version
	intent := adminChallengeIntent{Operation: "trip.admin_close", TargetID: &command.TargetID, ExpectedVersion: &version, Reason: &reason, AvailableData: payload.AvailableData}
	proof, ok := s.validateAdminProof(w, requestID, actor, payload.ChallengeID, "admin_close", intent)
	if !ok {
		return dataapi.CommandResult{}, false
	}
	if trip.Status != "active" && trip.Status != "returning" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	tripEmployeeID, employee, employeeFound := s.employeeByID(trip.EmployeeID)
	vehicleIndex := s.vehicleIndex(trip.VehicleID)
	if !employeeFound || vehicleIndex < 0 {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	draft, returnExists := s.currentDraftForTrip(trip)
	now := s.now().UTC()
	if !returnExists {
		draft = dataapi.Return{ID: newRequestID(), TripID: trip.ID, Status: "draft", Step: "admin_close", Version: 1, UpdatedAt: now,
			Inspection: dataapi.Inspection{ID: newRequestID(), Phase: "after", Status: "draft", OccupiedSlots: []int{}, MissingSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, Version: 1, UpdatedAt: now}}
	}
	inspection := draft.Inspection
	if payload.AvailableData != nil {
		if payload.AvailableData.FuelLevel != nil {
			inspection.FuelLevel = payload.AvailableData.FuelLevel
		}
		if payload.AvailableData.OdometerKM != nil {
			inspection.OdometerKM = payload.AvailableData.OdometerKM
		}
		if payload.AvailableData.KeysReturned != nil {
			inspection.KeysReturned = payload.AvailableData.KeysReturned
		}
		if payload.AvailableData.CarLocked != nil {
			inspection.CarLocked = payload.AvailableData.CarLocked
		}
		if payload.AvailableData.Latitude != nil && payload.AvailableData.Longitude != nil {
			var landmark *string
			if payload.AvailableData.Landmark != nil {
				copy := *payload.AvailableData.Landmark
				landmark = &copy
			}
			draft.ParkingLocation = &dataapi.ParkingLocation{ID: newRequestID(), Latitude: *payload.AvailableData.Latitude, Longitude: *payload.AvailableData.Longitude, Source: "admin", Landmark: landmark, ConfirmedAt: now}
		}
	}
	missing := adminCloseMissingData(inspection, draft.ParkingLocation)
	inspection.Status = "abandoned"
	inspection.Version++
	inspection.UpdatedAt = now
	draft.Inspection = inspection
	draft.Status = "admin_closed"
	draft.Step = "admin_closed"
	draft.Version++
	draft.UpdatedAt = now
	s.returns[draft.ID] = draft

	trip.Status = "closed_by_admin"
	trip.EndedAt = &now
	trip.ReturnID = &draft.ID
	trip.AfterInspection = &inspection
	trip.ParkingLocation = draft.ParkingLocation
	trip.MissingData = missing
	trip.Version++
	trip.UpdatedAt = now
	s.trips[trip.ID] = trip

	employee.ActiveTripID = nil
	employee.Version++
	employee.UpdatedAt = now
	s.employees[tripEmployeeID] = employee

	vehicle := &s.vehicles[vehicleIndex]
	if draft.ParkingLocation != nil {
		vehicle.CurrentParking = draft.ParkingLocation
	}
	if inspection.FuelLevel != nil {
		vehicle.CurrentFuel = inspection.FuelLevel
		vehicle.FuelConfirmedAt = &now
	}
	if inspection.OdometerKM != nil && (vehicle.CurrentOdometerKM == nil || *inspection.OdometerKM >= *vehicle.CurrentOdometerKM) {
		vehicle.CurrentOdometerKM = inspection.OdometerKM
		vehicle.OdometerConfirmedAt = &now
	}
	vehicle.NeedsReview = true
	vehicle.Status = "unavailable"
	vehicle.Version++
	vehicle.UpdatedAt = now
	s.consumeAdminProof(proof, now)
	return commandResult("trip.admin_close", trip), true
}

func (s *Server) validateAdminProof(w http.ResponseWriter, requestID, actor, challengeID, purpose string, intent adminChallengeIntent) (mockChallenge, bool) {
	if !validUUID(challengeID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return mockChallenge{}, false
	}
	proof, found := s.challenges[challengeID]
	if !found || proof.Actor != actor || proof.Public.Purpose != purpose || proof.AdminIntentSHA256 == "" || !proof.Solved || proof.Invalidated || proof.ProofConsumed {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "CHALLENGE_INVALID")
		return mockChallenge{}, false
	}
	if !s.now().UTC().Before(proof.Public.ExpiresAt) {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "CHALLENGE_EXPIRED")
		return mockChallenge{}, false
	}
	hash, err := adminIntentHash(intent)
	if err != nil || hash != proof.AdminIntentSHA256 {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "CHALLENGE_INVALID")
		return mockChallenge{}, false
	}
	return proof, true
}

func (s *Server) consumeAdminProof(proof mockChallenge, now time.Time) {
	proof.ProofConsumed = true
	proof.Public.Version++
	proof.Public.UpdatedAt = now
	s.challenges[proof.Public.ID] = proof
}

func (s *Server) vehicleIndex(vehicleID string) int {
	for i := range s.vehicles {
		if s.vehicles[i].ID == vehicleID {
			return i
		}
	}
	return -1
}

func (s *Server) employeeByID(employeeID string) (string, dataapi.Employee, bool) {
	for maxID, employee := range s.employees {
		if employee.ID == employeeID {
			return maxID, employee, true
		}
	}
	return "", dataapi.Employee{}, false
}

func (s *Server) currentDraftForTrip(trip dataapi.Trip) (dataapi.Return, bool) {
	if trip.ReturnID != nil {
		if draft, found := s.returns[*trip.ReturnID]; found && draft.Status == "draft" {
			return draft, true
		}
	}
	for _, draft := range s.returns {
		if draft.TripID == trip.ID && draft.Status == "draft" {
			return draft, true
		}
	}
	return dataapi.Return{}, false
}

func (s *Server) cancelHoldForVehicle(vehicleID string, now time.Time, reason string) {
	for id, checkout := range s.checkouts {
		if checkout.VehicleID != vehicleID || checkout.Status != "holding" {
			continue
		}
		checkout.Status = "cancelled"
		checkout.Step = reason
		checkout.Version++
		checkout.UpdatedAt = now
		if checkout.Inspection.Status == "draft" {
			checkout.Inspection.Status = "abandoned"
			checkout.Inspection.UpdatedAt = now
		}
		s.checkouts[id] = checkout
	}
}

func (s *Server) cancelHoldForEmployee(employeeID string, now time.Time, reason string) {
	for id, checkout := range s.checkouts {
		if checkout.EmployeeID != employeeID || checkout.Status != "holding" {
			continue
		}
		checkout.Status = "cancelled"
		checkout.Step = reason
		checkout.Version++
		checkout.UpdatedAt = now
		if checkout.Inspection.Status == "draft" {
			checkout.Inspection.Status = "abandoned"
			checkout.Inspection.UpdatedAt = now
		}
		s.checkouts[id] = checkout
		for index := range s.vehicles {
			vehicle := &s.vehicles[index]
			if vehicle.ID != checkout.VehicleID || vehicle.Status != "holding" {
				continue
			}
			if vehicle.ManualBlocked || vehicle.NeedsReview {
				vehicle.Status = "unavailable"
			} else {
				vehicle.Status = "available"
			}
			vehicle.Version++
			vehicle.UpdatedAt = now
			break
		}
	}
}

func (s *Server) hasBlockingIssue(vehicleID string) bool {
	for _, issue := range s.issues {
		if issue.VehicleID == vehicleID && issue.BlocksIssuance && issue.Status != "resolved" && issue.Status != "known_nonblocking" {
			return true
		}
	}
	return false
}

func validAdminReason(value string) bool {
	return len([]rune(value)) >= 1 && len([]rune(value)) <= 1000
}

func validAdminName(value string) bool { return len([]rune(value)) >= 1 && len([]rune(value)) <= 200 }

func validMockAdminCloseData(data *dataapi.AdminCloseData) bool {
	if data == nil {
		return true
	}
	if data.FuelLevel != nil && !(*data.FuelLevel == 0 || *data.FuelLevel == 25 || *data.FuelLevel == 50 || *data.FuelLevel == 75 || *data.FuelLevel == 100) || data.OdometerKM != nil && (*data.OdometerKM < 0 || *data.OdometerKM > 10_000_000) ||
		(data.Latitude == nil) != (data.Longitude == nil) || data.Latitude != nil && (*data.Latitude < -90 || *data.Latitude > 90) || data.Longitude != nil && (*data.Longitude < -180 || *data.Longitude > 180) || data.Landmark != nil && len([]rune(*data.Landmark)) > 500 {
		return false
	}
	return true
}

func validAdminClosePayloadShape(raw json.RawMessage) bool {
	if hasFields(raw, "reason", "challenge_id") {
		return true
	}
	if !hasExactFields(raw, "reason", "challenge_id", "available_data") {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	return strings.TrimSpace(string(fields["available_data"])) != "null"
}

func adminCloseMissingData(inspection dataapi.Inspection, parking *dataapi.ParkingLocation) []string {
	missing := make([]string, 0, 6)
	if len(inspection.OccupiedSlots) < 8 {
		missing = append(missing, "after_photos")
	}
	if inspection.FuelLevel == nil {
		missing = append(missing, "fuel_level")
	}
	if inspection.OdometerKM == nil {
		missing = append(missing, "odometer_km")
	}
	if parking == nil {
		missing = append(missing, "parking_location")
	}
	if inspection.KeysReturned == nil {
		missing = append(missing, "keys_returned")
	}
	if inspection.CarLocked == nil {
		missing = append(missing, "car_locked")
	}
	return missing
}
