package datamock

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func validConversationContext(context dataapi.ConversationContext) bool {
	for _, id := range []*string{context.TargetID, context.VehicleID, context.IssueID, context.ChallengeID, context.TripID, context.ReturnID} {
		if id != nil && !validUUID(*id) {
			return false
		}
	}
	if context.VehicleVersion != nil && *context.VehicleVersion < 1 || context.SelectedSlot != nil && (*context.SelectedSlot < 1 || *context.SelectedSlot > 8) || context.DraftText != nil && utf8.RuneCountInString(*context.DraftText) > 1000 || context.Cursor != nil && len(*context.Cursor) > 2048 || len(context.AssetIDs) > 3 {
		return false
	}
	if context.IssueCategory != nil && !validIssueCategory(*context.IssueCategory) {
		return false
	}
	seen := make(map[string]bool, len(context.AssetIDs))
	for _, id := range context.AssetIDs {
		if !validUUID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *Server) saveConversation(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var input dataapi.ConversationSaveInput
	if !strictPayload(command.Payload, &input) || strings.TrimSpace(input.Flow) == "" || len(input.Flow) > 80 || strings.TrimSpace(input.Step) == "" || len(input.Step) > 80 || !validConversationContext(input.Context) ||
		input.PendingInputKind != nil && *input.PendingInputKind != "text" && *input.PendingInputKind != "photo" && *input.PendingInputKind != "geo" && *input.PendingInputKind != "none" {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	employee, found := s.employees[actor]
	if !found || employee.ID != command.TargetID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	currentVersion := int64(1)
	if current, found := s.conversations[actor]; found {
		currentVersion = current.Version
	}
	if command.Version != currentVersion {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", currentVersion)
		return dataapi.CommandResult{}, false
	}
	valid := false
	switch input.Flow {
	case "issue_before":
		valid = s.validBeforeIssueConversation(actor, input)
	case "issue_during":
		valid = s.validDuringIssueConversation(actor, input)
	case "issue_after":
		valid = s.validAfterIssueConversation(actor, input)
	}
	if !valid {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	context := input.Context
	context.AssetIDs = append([]string(nil), input.Context.AssetIDs...)
	conversation := dataapi.Conversation{Flow: input.Flow, Step: input.Step, Context: context, PendingInputKind: input.PendingInputKind, Version: currentVersion + 1, UpdatedAt: s.now().UTC()}
	s.conversations[actor] = conversation
	return commandResult("conversation.save", conversation), true
}

func (s *Server) validAfterIssueConversation(actor string, input dataapi.ConversationSaveInput) bool {
	c := input.Context
	if c.TargetID == nil || c.TripID == nil || c.ReturnID == nil || c.VehicleID == nil || c.VehicleVersion == nil || c.IssueCategory == nil || c.DraftText == nil || strings.TrimSpace(*c.DraftText) == "" {
		return false
	}
	draft, found := s.returns[*c.ReturnID]
	if !found || draft.Inspection.ID != *c.TargetID || draft.TripID != *c.TripID {
		return false
	}
	trip, found := s.trips[*c.TripID]
	if !found || trip.EmployeeID != s.employees[actor].ID || trip.VehicleID != *c.VehicleID || trip.ReturnID == nil || *trip.ReturnID != draft.ID {
		return false
	}
	if input.Step == "done" {
		if c.IssueID == nil {
			return false
		}
		issue, found := s.issues[*c.IssueID]
		return found && issue.AuthorID == trip.EmployeeID && issue.TripID != nil && *issue.TripID == trip.ID && issue.InspectionID != nil && *issue.InspectionID == draft.Inspection.ID && issue.VehicleID == trip.VehicleID && issue.Stage == "after"
	}
	if input.Step != "collect_photos" && input.Step != "review" || trip.Status != "returning" || draft.Status != "draft" || draft.Inspection.Status != "draft" || draft.IntentConfirmedAt == nil || c.IssueID != nil {
		return false
	}
	for _, id := range c.AssetIDs {
		asset, found := s.issueAssets[id]
		if !found || asset.Actor != actor || asset.ScopeType != "inspection" || asset.ScopeID != draft.Inspection.ID || asset.AttachedIssueID != nil || !s.now().UTC().Before(asset.ExpiresAt) {
			return false
		}
	}
	return true
}

func (s *Server) validDuringIssueConversation(actor string, input dataapi.ConversationSaveInput) bool {
	c := input.Context
	if c.TargetID == nil || c.TripID == nil || *c.TargetID != *c.TripID || c.VehicleID == nil || c.VehicleVersion == nil || c.IssueCategory == nil || c.DraftText == nil || strings.TrimSpace(*c.DraftText) == "" || c.ReturnID != nil {
		return false
	}
	trip, found := s.trips[*c.TripID]
	if !found || trip.EmployeeID != s.employees[actor].ID || trip.VehicleID != *c.VehicleID {
		return false
	}
	if input.Step == "done" {
		if c.IssueID == nil {
			return false
		}
		issue, found := s.issues[*c.IssueID]
		return found && issue.AuthorID == trip.EmployeeID && issue.TripID != nil && *issue.TripID == trip.ID && issue.VehicleID == trip.VehicleID && issue.Stage == "during"
	}
	if input.Step != "collect_photos" && input.Step != "review" || trip.Status != "active" || c.IssueID != nil {
		return false
	}
	for _, id := range c.AssetIDs {
		asset, found := s.issueAssets[id]
		if !found || asset.Actor != actor || asset.ScopeType != "trip" || asset.ScopeID != trip.ID || asset.AttachedIssueID != nil || !s.now().UTC().Before(asset.ExpiresAt) {
			return false
		}
	}
	return true
}

func (s *Server) validBeforeIssueConversation(actor string, input dataapi.ConversationSaveInput) bool {
	context := input.Context
	if context.TargetID == nil || context.VehicleID == nil || context.VehicleVersion == nil || context.IssueCategory == nil || context.DraftText == nil || strings.TrimSpace(*context.DraftText) == "" {
		return false
	}
	if input.Step == "done" {
		if context.IssueID == nil {
			return false
		}
		issue, found := s.issues[*context.IssueID]
		return found && issue.AuthorID == s.employees[actor].ID && issue.VehicleID == *context.VehicleID && issue.InspectionID != nil && *issue.InspectionID == *context.TargetID
	}
	if input.Step != "collect_photos" && input.Step != "review" {
		return false
	}
	owned := false
	for _, checkout := range s.checkouts {
		if checkout.Inspection.ID == *context.TargetID && checkout.VehicleID == *context.VehicleID && checkout.EmployeeID == s.employees[actor].ID && checkout.Status == "holding" && checkout.Inspection.NewDamage != nil && *checkout.Inspection.NewDamage {
			owned = true
			break
		}
	}
	if !owned {
		return false
	}
	for _, id := range context.AssetIDs {
		asset, found := s.issueAssets[id]
		if !found || asset.Actor != actor || asset.ScopeType != "inspection" || asset.ScopeID != *context.TargetID || asset.AttachedIssueID != nil || !s.now().UTC().Before(asset.ExpiresAt) {
			return false
		}
	}
	return true
}
