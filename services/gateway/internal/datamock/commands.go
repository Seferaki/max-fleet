package datamock

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type commandRecord struct {
	Signature string
	Result    dataapi.CommandResult
}

type mockCommand struct {
	Operation string
	TargetID  string
	Version   int64
}

func parseCommand(body []byte) (mockCommand, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 4 {
		return mockCommand{}, false
	}
	for _, name := range []string{"operation", "target_id", "expected_version", "payload"} {
		if _, ok := fields[name]; !ok {
			return mockCommand{}, false
		}
	}
	var command mockCommand
	if json.Unmarshal(fields["operation"], &command.Operation) != nil || json.Unmarshal(fields["target_id"], &command.TargetID) != nil || json.Unmarshal(fields["expected_version"], &command.Version) != nil || !validUUID(command.TargetID) || command.Version < 1 {
		return mockCommand{}, false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(fields["payload"], &payload); err != nil || payload == nil || len(payload) != 0 {
		return mockCommand{}, false
	}
	return command, true
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request, requestID string) {
	key := r.Header.Get("Idempotency-Key")
	lease := r.Header.Get("X-Inbox-Lease")
	if len(key) < 8 || len(key) > 200 || strings.ContainsAny(key, "\r\n") || (r.Header.Get("X-Inbox-Event-ID") == "") != (lease == "") || (r.Header.Get("X-Inbox-Event-ID") != "" && !validUUID(r.Header.Get("X-Inbox-Event-ID"))) || len(lease) > 200 || strings.ContainsAny(lease, "\r\n") {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	command, ok := parseCommand(body)
	if !ok || (command.Operation != "checkout.create" && command.Operation != "checkout.cancel") {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	actor := r.Header.Get("X-Actor-Max-ID")
	identity := actor + ":" + key
	signature := command.Operation + ":" + command.TargetID + ":" + strconv.FormatInt(command.Version, 10)
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, exists := s.commands[identity]; exists {
		if record.Signature != signature {
			s.fail(w, requestID, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		s.success(w, requestID, record.Result)
		return
	}
	if !s.expireAndSave(w, requestID) {
		return
	}
	before := s.snapshot()
	var result dataapi.CommandResult
	if command.Operation == "checkout.create" {
		result, ok = s.createCheckout(w, requestID, actor, command)
	} else {
		result, ok = s.cancelCheckout(w, requestID, actor, command)
	}
	if !ok {
		return
	}
	s.commands[identity] = commandRecord{Signature: signature, Result: result}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return
	}
	s.success(w, requestID, result)
}

func (s *Server) createCheckout(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	employee := s.employees[actor]
	if !employee.CanStartTrip {
		s.fail(w, requestID, http.StatusForbidden, "CANNOT_START_TRIP")
		return dataapi.CommandResult{}, false
	}
	for _, checkout := range s.checkouts {
		if checkout.EmployeeID == employee.ID && checkout.Status == "holding" {
			s.fail(w, requestID, http.StatusConflict, "USER_BUSY")
			return dataapi.CommandResult{}, false
		}
	}
	for index := range s.vehicles {
		vehicle := &s.vehicles[index]
		if vehicle.ID != command.TargetID {
			continue
		}
		if vehicle.Status != "available" || vehicle.ManualBlocked || vehicle.NeedsReview || vehicle.CurrentParking == nil || vehicle.KeyInstructions == "" {
			s.fail(w, requestID, http.StatusConflict, "VEHICLE_UNAVAILABLE")
			return dataapi.CommandResult{}, false
		}
		if vehicle.Version != command.Version {
			s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", vehicle.Version)
			return dataapi.CommandResult{}, false
		}
		now := s.now().UTC()
		checkoutID, inspectionID := newRequestID(), newRequestID()
		checkout := dataapi.Checkout{ID: checkoutID, VehicleID: vehicle.ID, EmployeeID: employee.ID, Status: "holding", Step: "math", ExpiresAt: now.Add(15 * time.Minute), Version: 1, UpdatedAt: now,
			Inspection: dataapi.Inspection{ID: inspectionID, Phase: "before", Status: "draft", OccupiedSlots: []int{}, MissingSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, Version: 1, UpdatedAt: now}}
		s.checkouts[checkoutID] = checkout
		vehicle.Status = "holding"
		vehicle.Version++
		vehicle.UpdatedAt = now
		return commandResult("checkout.create", checkout), true
	}
	s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
	return dataapi.CommandResult{}, false
}

func (s *Server) cancelCheckout(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	checkout, found := s.checkouts[command.TargetID]
	if !found || checkout.EmployeeID != s.employees[actor].ID {
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
	if checkout.Status != "holding" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	checkout.Status = "cancelled"
	checkout.Version++
	checkout.UpdatedAt = now
	s.checkouts[checkout.ID] = checkout
	for index := range s.vehicles {
		if s.vehicles[index].ID == checkout.VehicleID {
			s.vehicles[index].Status = "available"
			s.vehicles[index].Version++
			s.vehicles[index].UpdatedAt = now
			break
		}
	}
	return commandResult("checkout.cancel", checkout), true
}

func (s *Server) commandResult(w http.ResponseWriter, r *http.Request, requestID string) {
	key, operation := r.PathValue("key"), r.URL.Query().Get("operation")
	if len(key) < 8 || len(key) > 200 || operation == "" {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.commands[r.Header.Get("X-Actor-Max-ID")+":"+key]
	if !found || record.Result.Operation != operation {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, record.Result)
}

func (s *Server) expireHolds() bool {
	now := s.now().UTC()
	changed := false
	for id, checkout := range s.checkouts {
		if checkout.Status != "holding" || now.Before(checkout.ExpiresAt) {
			continue
		}
		checkout.Status = "expired"
		changed = true
		checkout.Version++
		checkout.UpdatedAt = now
		s.checkouts[id] = checkout
		for index := range s.vehicles {
			if s.vehicles[index].ID == checkout.VehicleID {
				s.vehicles[index].Status = "available"
				s.vehicles[index].Version++
				s.vehicles[index].UpdatedAt = now
				break
			}
		}
	}
	return changed
}

func commandResult(operation string, checkout dataapi.Checkout) dataapi.CommandResult {
	encoded, _ := json.Marshal(checkout)
	return dataapi.CommandResult{Operation: operation, Aggregate: encoded}
}

func (s *Server) failVersion(w http.ResponseWriter, requestID string, status int, code string, version int64) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": code, "retryable": false, "details": map[string]any{"current_version": version}}, "request_id": requestID})
}
