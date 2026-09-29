package datamock

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type vehicleSnapshotCorrectionAudit struct {
	ID               string                   `json:"id"`
	ActorEmployeeID  string                   `json:"actor_employee_id"`
	VehicleID        string                   `json:"vehicle_id"`
	AssignmentKind   string                   `json:"assignment_kind"`
	AssignmentID     *string                  `json:"assignment_id"`
	Reason           string                   `json:"reason"`
	ChangedFields    []string                 `json:"changed_fields"`
	BeforeFuelLevel  *int                     `json:"before_fuel_level"`
	AfterFuelLevel   *int                     `json:"after_fuel_level"`
	BeforeOdometerKM *int64                   `json:"before_odometer_km"`
	AfterOdometerKM  *int64                   `json:"after_odometer_km"`
	BeforeParking    *dataapi.ParkingLocation `json:"before_parking"`
	AfterParking     *dataapi.ParkingLocation `json:"after_parking"`
	CreatedAt        time.Time                `json:"created_at"`
}

type correctionAssignment struct {
	kind string
	id   string
}

func validVehicleCorrectionShape(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) < 3 || len(fields) > 5 {
		return false
	}
	for key, value := range fields {
		if key != "reason" && key != "fuel_level" && key != "odometer_km" && key != "location" && key != "confirmation" {
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	_, reason := fields["reason"]
	_, confirmation := fields["confirmation"]
	_, fuel := fields["fuel_level"]
	_, odometer := fields["odometer_km"]
	_, location := fields["location"]
	return reason && confirmation && (fuel || odometer || location)
}

func validCorrectionLocation(location *dataapi.LocationInput) bool {
	return location != nil && !math.IsNaN(location.Latitude) && !math.IsInf(location.Latitude, 0) &&
		location.Latitude >= -90 && location.Latitude <= 90 &&
		!math.IsNaN(location.Longitude) && !math.IsInf(location.Longitude, 0) &&
		location.Longitude >= -180 && location.Longitude <= 180 && location.Confirmed &&
		(location.Source == "max_geo" || location.Source == "manual_map" || location.Source == "admin") &&
		(location.Landmark == nil || utf8.RuneCountInString(*location.Landmark) <= 500)
}

func (s *Server) correctVehicleSnapshot(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var input dataapi.VehicleSnapshotCorrectionInput
	if !validVehicleCorrectionShape(command.Payload) || !strictPayload(command.Payload, &input) ||
		!validAdminReason(input.Reason) || !input.Confirmation ||
		input.FuelLevel != nil && !validFuelLevel(*input.FuelLevel) ||
		input.OdometerKM != nil && *input.OdometerKM < 0 ||
		input.Location != nil && !validCorrectionLocation(input.Location) {
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
	assignment, active, consistent := s.vehicleCorrectionAssignment(*vehicle)
	if !consistent {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	if active {
		if input.OdometerKM == nil || input.FuelLevel != nil || input.Location != nil {
			s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
			return dataapi.CommandResult{}, false
		}
	} else if vehicle.Status != "available" && vehicle.Status != "unavailable" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}

	changed := make([]string, 0, 3)
	if input.FuelLevel != nil && (vehicle.CurrentFuel == nil || *vehicle.CurrentFuel != *input.FuelLevel) {
		changed = append(changed, "fuel_level")
	}
	if input.OdometerKM != nil && (vehicle.CurrentOdometerKM == nil || *vehicle.CurrentOdometerKM != *input.OdometerKM) {
		changed = append(changed, "odometer_km")
	}
	if input.Location != nil && !sameCorrectionParking(vehicle.CurrentParking, input.Location) {
		changed = append(changed, "location")
	}
	if len(changed) == 0 {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}

	now := s.now().UTC()
	audit := vehicleSnapshotCorrectionAudit{
		ID: newRequestID(), ActorEmployeeID: s.employees[actor].ID, VehicleID: vehicle.ID,
		AssignmentKind: assignment.kind, Reason: input.Reason, ChangedFields: changed, CreatedAt: now,
	}
	if assignment.id != "" {
		assignmentID := assignment.id
		audit.AssignmentID = &assignmentID
	}
	if correctionContains(changed, "fuel_level") {
		audit.BeforeFuelLevel = copyCorrectionInt(vehicle.CurrentFuel)
		audit.AfterFuelLevel = copyCorrectionInt(input.FuelLevel)
		fuel := *input.FuelLevel
		vehicle.CurrentFuel = &fuel
		vehicle.FuelConfirmedAt = &now
	}
	if correctionContains(changed, "odometer_km") {
		audit.BeforeOdometerKM = copyCorrectionInt64(vehicle.CurrentOdometerKM)
		audit.AfterOdometerKM = copyCorrectionInt64(input.OdometerKM)
		odometer := *input.OdometerKM
		vehicle.CurrentOdometerKM = &odometer
		vehicle.OdometerConfirmedAt = &now
	}
	if correctionContains(changed, "location") {
		audit.BeforeParking = copyCorrectionParking(vehicle.CurrentParking)
		parking := correctionParking(*input.Location, now)
		vehicle.CurrentParking = parking
		audit.AfterParking = copyCorrectionParking(parking)
	}
	vehicle.Version++
	vehicle.UpdatedAt = now
	s.vehicleCorrections = append(s.vehicleCorrections, audit)
	return commandResult("vehicle.correct_snapshot", *vehicle), true
}

func (s *Server) vehicleCorrectionAssignment(vehicle dataapi.Vehicle) (correctionAssignment, bool, bool) {
	var holds []dataapi.Checkout
	var trips []dataapi.Trip
	for _, checkout := range s.checkouts {
		if checkout.VehicleID == vehicle.ID && checkout.Status == "holding" {
			holds = append(holds, checkout)
		}
	}
	for _, trip := range s.trips {
		if trip.VehicleID == vehicle.ID && (trip.Status == "active" || trip.Status == "returning") {
			trips = append(trips, trip)
		}
	}
	if len(holds)+len(trips) == 0 {
		return correctionAssignment{}, false, vehicle.Status != "holding" && vehicle.Status != "in_trip"
	}
	if vehicle.Status == "holding" && len(holds) == 1 && len(trips) == 0 {
		_, _, found := s.employeeByID(holds[0].EmployeeID)
		if !found {
			return correctionAssignment{}, true, false
		}
		return correctionAssignment{kind: "checkout", id: holds[0].ID}, true, true
	}
	if vehicle.Status == "in_trip" && len(holds) == 0 && len(trips) == 1 {
		trip := trips[0]
		_, employee, found := s.employeeByID(trip.EmployeeID)
		if !found || employee.ActiveTripID == nil || *employee.ActiveTripID != trip.ID {
			return correctionAssignment{}, true, false
		}
		switch trip.Status {
		case "active":
			if trip.ReturnID != nil {
				return correctionAssignment{}, true, false
			}
		case "returning":
			if trip.ReturnID == nil {
				return correctionAssignment{}, true, false
			}
			draft, ok := s.returns[*trip.ReturnID]
			if !ok || draft.TripID != trip.ID || draft.Status != "draft" || draft.IntentConfirmedAt == nil {
				return correctionAssignment{}, true, false
			}
		}
		return correctionAssignment{kind: "trip", id: trip.ID}, true, true
	}
	return correctionAssignment{}, true, false
}

func sameCorrectionParking(current *dataapi.ParkingLocation, input *dataapi.LocationInput) bool {
	if current == nil || input == nil || current.Latitude != input.Latitude ||
		current.Longitude != input.Longitude || current.Source != input.Source {
		return false
	}
	if current.Landmark == nil || input.Landmark == nil {
		return current.Landmark == nil && input.Landmark == nil
	}
	return *current.Landmark == *input.Landmark
}

func correctionParking(input dataapi.LocationInput, now time.Time) *dataapi.ParkingLocation {
	var landmark *string
	if input.Landmark != nil {
		value := *input.Landmark
		landmark = &value
	}
	return &dataapi.ParkingLocation{ID: newRequestID(), Latitude: input.Latitude, Longitude: input.Longitude, Source: input.Source, Landmark: landmark, ConfirmedAt: now}
}

func copyCorrectionInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyCorrectionInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyCorrectionParking(value *dataapi.ParkingLocation) *dataapi.ParkingLocation {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Landmark != nil {
		landmark := *value.Landmark
		copy.Landmark = &landmark
	}
	return &copy
}

func validVehicleSnapshotCorrectionAudit(audit vehicleSnapshotCorrectionAudit) bool {
	if !validUUID(audit.ID) || !validUUID(audit.ActorEmployeeID) || !validUUID(audit.VehicleID) ||
		!validAdminReason(audit.Reason) || audit.CreatedAt.IsZero() || len(audit.ChangedFields) == 0 {
		return false
	}
	if audit.AssignmentKind == "" {
		if audit.AssignmentID != nil {
			return false
		}
	} else if (audit.AssignmentKind != "checkout" && audit.AssignmentKind != "trip") ||
		audit.AssignmentID == nil || !validUUID(*audit.AssignmentID) {
		return false
	}
	seen := make(map[string]bool, len(audit.ChangedFields))
	for _, field := range audit.ChangedFields {
		if seen[field] {
			return false
		}
		seen[field] = true
		switch field {
		case "fuel_level":
			if audit.AfterFuelLevel == nil || !validFuelLevel(*audit.AfterFuelLevel) || audit.BeforeFuelLevel != nil && !validFuelLevel(*audit.BeforeFuelLevel) {
				return false
			}
		case "odometer_km":
			if audit.AfterOdometerKM == nil || *audit.AfterOdometerKM < 0 || audit.BeforeOdometerKM != nil && *audit.BeforeOdometerKM < 0 {
				return false
			}
		case "location":
			if audit.AfterParking == nil || audit.AfterParking.ID == "" || audit.BeforeParking != nil && audit.BeforeParking.ID == "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func correctionContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
