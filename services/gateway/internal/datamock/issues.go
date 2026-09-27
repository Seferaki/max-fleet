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
	if !strictPayload(command.Payload, &input) || !validIssueCategory(input.Category) || strings.TrimSpace(input.Description) == "" || len(input.Description) > 1000 || len(input.AssetIDs) > 3 || input.AssetIDs == nil || (input.TripID == nil) == (input.InspectionID == nil) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if input.TripID != nil && !validUUID(*input.TripID) || input.InspectionID != nil && !validUUID(*input.InspectionID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	checkoutID := ""
	var checkout dataapi.Checkout
	returnID := ""
	var draft dataapi.Return
	tripID := ""
	var trip dataapi.Trip
	if input.InspectionID != nil {
		for id, candidate := range s.checkouts {
			if candidate.Inspection.ID == *input.InspectionID {
				checkout, checkoutID = candidate, id
				break
			}
		}
		if checkoutID == "" {
			for id, candidate := range s.returns {
				if candidate.Inspection.ID == *input.InspectionID {
					draft, returnID = candidate, id
					tripID = draft.TripID
					trip = s.trips[tripID]
					break
				}
			}
		}
	} else {
		tripID = *input.TripID
		trip = s.trips[tripID]
	}
	employee := s.employees[actor]
	if checkoutID == "" && tripID == "" || checkoutID != "" && (checkout.EmployeeID != employee.ID || checkout.VehicleID != command.TargetID) || tripID != "" && (trip.ID == "" || trip.EmployeeID != employee.ID || trip.VehicleID != command.TargetID) {
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
	if checkoutID != "" && (checkout.Status != "holding" || checkout.Inspection.Status != "draft" || vehicle.Status != "holding") || tripID != "" && (trip.Status != "active" && trip.Status != "returning" || vehicle.Status != "in_trip") || returnID != "" && (draft.Status != "draft" || draft.Inspection.Status != "draft" || draft.IntentConfirmedAt == nil || trip.ReturnID == nil || *trip.ReturnID != draft.ID) {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	seenAssets := make(map[string]bool, len(input.AssetIDs))
	for _, id := range input.AssetIDs {
		if !validUUID(id) || seenAssets[id] {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return dataapi.CommandResult{}, false
		}
		seenAssets[id] = true
		asset, found := s.issueAssets[id]
		if !found || asset.Actor != actor || asset.AttachedIssueID != nil || !now.Before(asset.ExpiresAt) || !(asset.ScopeType == "vehicle" && asset.ScopeID == vehicle.ID || asset.ScopeType == "trip" && asset.ScopeID == tripID && tripID != "" || asset.ScopeType == "inspection" && input.InspectionID != nil && asset.ScopeID == *input.InspectionID) {
			s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
			return dataapi.CommandResult{}, false
		}
	}
	stage := "before"
	if tripID != "" {
		stage = "during"
		if trip.Status == "returning" {
			stage = "return"
		}
		if returnID != "" {
			stage = "after"
		}
	}
	issue := dataapi.Issue{ID: newRequestID(), VehicleID: vehicle.ID, AuthorID: employee.ID, Stage: stage, Category: input.Category, Description: input.Description, Status: "open", BlocksIssuance: true, InspectionID: input.InspectionID, AssetIDs: append([]string{}, input.AssetIDs...), Version: 1, UpdatedAt: now}
	if tripID != "" {
		issue.TripID = &tripID
	}
	s.issues[issue.ID] = issue
	for _, id := range input.AssetIDs {
		asset := s.issueAssets[id]
		asset.AttachedIssueID = &issue.ID
		s.issueAssets[id] = asset
	}
	if checkoutID != "" {
		checkout.Status = "rejected"
		checkout.Step = "issue_reported"
		checkout.Inspection.Status = "abandoned"
		checkout.Inspection.Version++
		checkout.Inspection.UpdatedAt = now
		checkout.Version++
		checkout.UpdatedAt = now
		s.checkouts[checkoutID] = checkout
		vehicle.Status = "unavailable"
	} else {
		trip.Issues = append(trip.Issues, issue)
		trip.Version++
		trip.UpdatedAt = now
		s.trips[tripID] = trip
		if returnID != "" {
			draft.Version++
			draft.UpdatedAt = now
			s.returns[returnID] = draft
		}
	}
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
