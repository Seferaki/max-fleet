package datamock

import (
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func validConversationFlow(flow string) bool {
	switch flow {
	case "issue_before", "issue_during", "issue_after", "return_location", "issue_post_return", "issue_admin_resolution", "vehicle_odometer_correction", "trip_admin_close":
		return true
	}
	return false
}

func validConversationContext(context dataapi.ConversationContext) bool {
	for _, id := range []*string{context.TargetID, context.VehicleID, context.IssueID, context.ChallengeID, context.TripID, context.ReturnID} {
		if id != nil && !validUUID(*id) {
			return false
		}
	}
	if context.VehicleVersion != nil && *context.VehicleVersion < 1 || context.IssueVersion != nil && *context.IssueVersion < 1 || context.TripVersion != nil && *context.TripVersion < 1 || context.ChallengeVersion != nil && *context.ChallengeVersion < 1 || context.SelectedSlot != nil && (*context.SelectedSlot < 1 || *context.SelectedSlot > 8) || context.CorrectionOdometerKM != nil && *context.CorrectionOdometerKM < 0 || context.DraftText != nil && utf8.RuneCountInString(*context.DraftText) > 1000 || context.Cursor != nil && len(*context.Cursor) > 2048 || len(context.AssetIDs) > 3 || context.ChallengeQuestion != nil && utf8.RuneCountInString(*context.ChallengeQuestion) > 200 || len(context.ChallengeOptions) > 4 {
		return false
	}
	if context.AdminCloseData != nil && !validMockAdminCloseData(context.AdminCloseData) || context.ChallengeExpiresAt != nil && context.ChallengeID == nil {
		return false
	}
	seenOptions := make(map[int]bool, len(context.ChallengeOptions))
	for _, option := range context.ChallengeOptions {
		if option < 0 || option > 18 || seenOptions[option] {
			return false
		}
		seenOptions[option] = true
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
	if !strictPayload(command.Payload, &input) || !validConversationFlow(input.Flow) || strings.TrimSpace(input.Step) == "" || len(input.Step) > 80 || !validConversationContext(input.Context) ||
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
	if input.Flow != "trip_admin_close" && (input.Context.TripVersion != nil || input.Context.AdminCloseData != nil || input.Context.ChallengeVersion != nil || input.Context.ChallengeQuestion != nil || len(input.Context.ChallengeOptions) != 0 || input.Context.ChallengeExpiresAt != nil) {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
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
	case "issue_post_return":
		valid = s.validPostReturnIssueConversation(actor, input)
	case "return_location":
		valid = s.validReturnLocationConversation(actor, input)
	case "issue_admin_resolution":
		valid = s.validAdminIssueResolutionConversation(actor, input)
	case "vehicle_odometer_correction":
		valid = s.validVehicleOdometerCorrectionConversation(actor, input)
	case "trip_admin_close":
		valid = s.validTripAdminCloseConversation(actor, input)
	}
	if !valid {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	context := input.Context
	context.AssetIDs = append([]string(nil), input.Context.AssetIDs...)
	context.ChallengeOptions = append([]int(nil), input.Context.ChallengeOptions...)
	conversation := dataapi.Conversation{Flow: input.Flow, Step: input.Step, Context: context, PendingInputKind: input.PendingInputKind, Version: currentVersion + 1, UpdatedAt: s.now().UTC()}
	s.conversations[actor] = conversation
	return commandResult("conversation.save", conversation), true
}

func (s *Server) validTripAdminCloseConversation(actor string, input dataapi.ConversationSaveInput) bool {
	employee, found := s.employees[actor]
	c := input.Context
	if !found || employee.Role != "admin" || c.TripID == nil || c.TripVersion == nil || *c.TripVersion < 1 ||
		c.TargetID != nil || c.VehicleID != nil || c.VehicleVersion != nil || c.IssueID != nil || c.IssueVersion != nil ||
		c.ReturnID != nil || c.IssueCategory != nil || c.SelectedSlot != nil || c.Cursor != nil || c.CorrectionOdometerKM != nil || len(c.AssetIDs) != 0 {
		return false
	}
	trip, exists := s.trips[*c.TripID]
	if !exists {
		return false
	}
	activeTrip := trip.Status == "active" || trip.Status == "returning"
	noChallenge := c.ChallengeID == nil && c.ChallengeVersion == nil && c.ChallengeQuestion == nil && len(c.ChallengeOptions) == 0 && c.ChallengeExpiresAt == nil
	noCloseDraft := c.DraftText == nil && c.AdminCloseData == nil
	pending := input.PendingInputKind
	if input.Step == "await_details" {
		return activeTrip && trip.Version == *c.TripVersion && noCloseDraft && noChallenge && pending != nil && *pending == "text"
	}
	if input.Step == "cancelled" {
		return trip.Status != "closed_by_admin" && trip.Version >= *c.TripVersion && noCloseDraft && noChallenge && pending != nil && *pending == "none"
	}
	if input.Step == "done" {
		return trip.Status == "closed_by_admin" && trip.Version == *c.TripVersion+1 && noCloseDraft && noChallenge && pending != nil && *pending == "none"
	}
	if !activeTrip || trip.Version != *c.TripVersion || c.DraftText == nil || strings.TrimSpace(*c.DraftText) == "" || c.AdminCloseData == nil || pending == nil || *pending != "none" {
		return false
	}
	if input.Step == "confirm" {
		return noChallenge
	}
	if input.Step != "challenge" || c.ChallengeID == nil || c.ChallengeVersion == nil || c.ChallengeQuestion == nil ||
		strings.TrimSpace(*c.ChallengeQuestion) == "" || len(c.ChallengeOptions) != 4 || c.ChallengeExpiresAt == nil {
		return false
	}
	challenge, exists := s.challenges[*c.ChallengeID]
	if !exists || challenge.Actor != actor || challenge.Public.ID != *c.ChallengeID || challenge.Public.Purpose != "admin_close" ||
		challenge.Public.Version != *c.ChallengeVersion || challenge.Public.Question != *c.ChallengeQuestion ||
		!slices.Equal(challenge.Public.Options, c.ChallengeOptions) || !challenge.Public.ExpiresAt.Equal(*c.ChallengeExpiresAt) || challenge.AdminTargetID != *c.TripID {
		return false
	}
	version, reason := *c.TripVersion, *c.DraftText
	intent := adminChallengeIntent{Operation: "trip.admin_close", TargetID: c.TripID, ExpectedVersion: &version, Reason: &reason, AvailableData: c.AdminCloseData}
	hash, err := adminIntentHash(intent)
	return err == nil && hash == challenge.AdminIntentSHA256
}

func (s *Server) validAdminIssueResolutionConversation(actor string, input dataapi.ConversationSaveInput) bool {
	employee, found := s.employees[actor]
	c := input.Context
	if !found || employee.Role != "admin" || c.IssueID == nil || c.IssueVersion == nil ||
		c.TargetID != nil || c.VehicleID != nil || c.VehicleVersion != nil || c.CorrectionOdometerKM != nil || c.IssueCategory != nil || c.SelectedSlot != nil ||
		c.ChallengeID != nil || c.TripID != nil || c.ReturnID != nil || c.Cursor != nil || len(c.AssetIDs) != 0 {
		return false
	}
	issue, found := s.issues[*c.IssueID]
	if !found || issue.Version != *c.IssueVersion {
		return false
	}
	statusCanResolve := issue.Status == "open" || issue.Status == "in_progress"
	switch input.Step {
	case "await_comment_resolved", "await_comment_known_nonblocking":
		return statusCanResolve && c.DraftText == nil && input.PendingInputKind != nil && *input.PendingInputKind == "text"
	case "confirm_resolved", "confirm_known_nonblocking":
		return statusCanResolve && c.DraftText != nil && strings.TrimSpace(*c.DraftText) != "" && input.PendingInputKind != nil && *input.PendingInputKind == "none"
	case "done":
		return (issue.Status == "resolved" || issue.Status == "known_nonblocking") && c.DraftText == nil && input.PendingInputKind != nil && *input.PendingInputKind == "none"
	case "cancelled":
		return statusCanResolve && c.DraftText == nil && input.PendingInputKind != nil && *input.PendingInputKind == "none"
	default:
		return false
	}
}

func (s *Server) validVehicleOdometerCorrectionConversation(actor string, input dataapi.ConversationSaveInput) bool {
	employee, found := s.employees[actor]
	c := input.Context
	if !found || employee.Role != "admin" || c.VehicleID == nil || c.VehicleVersion == nil || *c.VehicleVersion < 1 ||
		c.TargetID != nil || c.IssueID != nil || c.IssueVersion != nil || c.IssueCategory != nil || c.SelectedSlot != nil ||
		c.ChallengeID != nil || c.TripID != nil || c.ReturnID != nil || c.Cursor != nil || len(c.AssetIDs) != 0 {
		return false
	}
	index := s.vehicleIndex(*c.VehicleID)
	if index < 0 {
		return false
	}
	vehicle := s.vehicles[index]
	pending := input.PendingInputKind
	noPending := pending != nil && *pending == "none"
	switch input.Step {
	case "await_value":
		_, active, consistent := s.vehicleCorrectionAssignment(vehicle)
		return active && consistent && vehicle.Version == *c.VehicleVersion && c.CorrectionOdometerKM == nil && c.DraftText == nil &&
			pending != nil && *pending == "text"
	case "confirm":
		_, active, consistent := s.vehicleCorrectionAssignment(vehicle)
		return active && consistent && vehicle.Version == *c.VehicleVersion && c.CorrectionOdometerKM != nil && c.DraftText != nil &&
			strings.TrimSpace(*c.DraftText) != "" && noPending
	case "done", "cancelled":
		return vehicle.Version >= *c.VehicleVersion && c.CorrectionOdometerKM == nil && c.DraftText == nil && noPending
	default:
		return false
	}
}

func (s *Server) validPostReturnIssueConversation(actor string, input dataapi.ConversationSaveInput) bool {
	c := input.Context
	if c.TargetID == nil || c.TripID == nil || *c.TargetID != *c.TripID || c.VehicleID == nil || c.VehicleVersion == nil || *c.VehicleVersion < 1 || c.IssueCategory == nil || !validIssueCategory(*c.IssueCategory) || c.ReturnID != nil || c.CorrectionOdometerKM != nil {
		return false
	}
	trip, found := s.trips[*c.TripID]
	if !found || trip.EmployeeID != s.employees[actor].ID || trip.Status != "completed" || trip.VehicleID != *c.VehicleID {
		return false
	}
	vehicleIndex := -1
	for i := range s.vehicles {
		if s.vehicles[i].ID == trip.VehicleID {
			vehicleIndex = i
			break
		}
	}
	if vehicleIndex < 0 {
		return false
	}
	vehicle := s.vehicles[vehicleIndex]
	if input.Step == "done" {
		if c.IssueID == nil || c.VehicleVersion == nil || vehicle.Version <= *c.VehicleVersion {
			return false
		}
		issue, found := s.issues[*c.IssueID]
		return found && issue.AuthorID == trip.EmployeeID && issue.VehicleID == trip.VehicleID && issue.TripID != nil && *issue.TripID == trip.ID && issue.InspectionID == nil && issue.Stage == "post_return" && issue.Status == "open" && slices.Equal(issue.AssetIDs, c.AssetIDs)
	}
	if c.IssueID != nil || vehicle.Version != *c.VehicleVersion || vehicle.Status == "holding" || vehicle.Status == "in_trip" {
		return false
	}
	if input.Step == "awaiting_description" {
		return c.DraftText == nil && len(c.AssetIDs) == 0
	}
	if input.Step != "collect_photos" || c.DraftText == nil || strings.TrimSpace(*c.DraftText) == "" {
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

func parseDraftCoordinates(value string) (float64, float64, bool) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	lat, latErr := strconv.ParseFloat(parts[0], 64)
	lon, lonErr := strconv.ParseFloat(parts[1], 64)
	return lat, lon, latErr == nil && lonErr == nil && !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func (s *Server) validReturnLocationConversation(actor string, input dataapi.ConversationSaveInput) bool {
	c := input.Context
	if c.TargetID == nil || c.ReturnID == nil || *c.TargetID != *c.ReturnID || c.TripID == nil || c.VehicleID == nil || c.DraftText == nil || c.IssueID != nil || c.IssueCategory != nil || c.CorrectionOdometerKM != nil || len(c.AssetIDs) != 0 {
		return false
	}
	lat, lon, valid := parseDraftCoordinates(*c.DraftText)
	if !valid {
		return false
	}
	draft, found := s.returns[*c.ReturnID]
	if !found || draft.TripID != *c.TripID {
		return false
	}
	trip, found := s.trips[*c.TripID]
	if !found || trip.EmployeeID != s.employees[actor].ID || trip.VehicleID != *c.VehicleID || trip.ReturnID == nil || *trip.ReturnID != draft.ID {
		return false
	}
	if input.Step == "done" {
		return draft.ParkingLocation != nil && draft.ParkingLocation.Source == "max_geo" && draft.ParkingLocation.Latitude == lat && draft.ParkingLocation.Longitude == lon
	}
	return input.Step == "confirm" && draft.Status == "draft" && draft.IntentConfirmedAt != nil && trip.Status == "returning"
}

func (s *Server) validAfterIssueConversation(actor string, input dataapi.ConversationSaveInput) bool {
	c := input.Context
	if c.TargetID == nil || c.TripID == nil || c.ReturnID == nil || c.VehicleID == nil || c.VehicleVersion == nil || c.IssueCategory == nil || c.DraftText == nil || strings.TrimSpace(*c.DraftText) == "" || c.CorrectionOdometerKM != nil {
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
	if c.TargetID == nil || c.TripID == nil || *c.TargetID != *c.TripID || c.VehicleID == nil || c.VehicleVersion == nil || c.IssueCategory == nil || c.DraftText == nil || strings.TrimSpace(*c.DraftText) == "" || c.ReturnID != nil || c.CorrectionOdometerKM != nil {
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
	if context.TargetID == nil || context.VehicleID == nil || context.VehicleVersion == nil || context.IssueCategory == nil || context.DraftText == nil || strings.TrimSpace(*context.DraftText) == "" || context.CorrectionOdometerKM != nil {
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
