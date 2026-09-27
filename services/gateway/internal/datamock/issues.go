package datamock

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func validIssueCategory(category string) bool {
	switch category {
	case "body_damage", "mechanical", "cleanliness", "keys", "other":
		return true
	}
	return false
}

func (s *Server) createIssue(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(command.Payload, &fields)
	var input dataapi.IssueCreateInput
	if _, ok := fields["category"]; !ok {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if _, ok := fields["description"]; !ok {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if _, ok := fields["asset_ids"]; !ok {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !strictPayload(command.Payload, &input) || !validIssueCategory(input.Category) || strings.TrimSpace(input.Description) == "" || len(input.Description) > 1000 || len(input.AssetIDs) != 0 || input.AssetIDs == nil || (input.TripID == nil) == (input.InspectionID == nil) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if input.TripID != nil || !validUUID(*input.InspectionID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	checkoutID := ""
	var checkout dataapi.Checkout
	for id, candidate := range s.checkouts {
		if candidate.Inspection.ID == *input.InspectionID {
			checkout, checkoutID = candidate, id
			break
		}
	}
	if checkoutID == "" || checkout.EmployeeID != s.employees[actor].ID || checkout.VehicleID != command.TargetID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	vehicleIndex := -1
	for i := range s.vehicles {
		if s.vehicles[i].ID == command.TargetID {
			vehicleIndex = i
			break
		}
	}
	if vehicleIndex < 0 {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	vehicle := &s.vehicles[vehicleIndex]
	if vehicle.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", vehicle.Version)
		return dataapi.CommandResult{}, false
	}
	if checkout.Status != "holding" || checkout.Inspection.Status != "draft" || vehicle.Status != "holding" {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	issue := dataapi.Issue{ID: newRequestID(), VehicleID: vehicle.ID, AuthorID: checkout.EmployeeID, Stage: "before", Category: input.Category, Description: input.Description, Status: "open", BlocksIssuance: true, InspectionID: input.InspectionID, AssetIDs: []string{}, Version: 1, UpdatedAt: now}
	s.issues[issue.ID] = issue
	checkout.Status = "rejected"
	checkout.Step = "issue_reported"
	checkout.Inspection.Status = "abandoned"
	checkout.Inspection.Version++
	checkout.Inspection.UpdatedAt = now
	checkout.Version++
	checkout.UpdatedAt = now
	s.checkouts[checkoutID] = checkout
	vehicle.Status = "unavailable"
	vehicle.NeedsReview = true
	vehicle.Version++
	vehicle.UpdatedAt = now
	return commandResult("issue.create", issue), true
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	issue, found := s.issues[id]
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	if !found || issue.AuthorID != employee.ID && employee.Role != "admin" {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, issue)
}
