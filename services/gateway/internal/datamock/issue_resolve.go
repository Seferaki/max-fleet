package datamock

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type issueResolvePayload struct {
	Status       string `json:"status"`
	Comment      string `json:"comment"`
	Confirmation bool   `json:"confirmation"`
}

type issueActionAudit struct {
	IssueID         string    `json:"issue_id"`
	ActorEmployeeID string    `json:"actor_employee_id"`
	PreviousStatus  string    `json:"previous_status"`
	NextStatus      string    `json:"next_status"`
	Comment         string    `json:"comment"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Server) resolveIssue(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var input issueResolvePayload
	validPayload := strictPayload(command.Payload, &input)
	switch input.Status {
	case "in_progress":
		validPayload = validPayload && hasFields(command.Payload, "status") && input.Comment == "" && !input.Confirmation
	case "resolved", "known_nonblocking":
		validPayload = validPayload && hasFields(command.Payload, "status", "comment", "confirmation") &&
			strings.TrimSpace(input.Comment) != "" && utf8.RuneCountInString(input.Comment) <= 1000 && input.Confirmation
	default:
		validPayload = false
	}
	if !validPayload {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.requireAdmin(w, requestID, actor) {
		return dataapi.CommandResult{}, false
	}
	issue, exists := s.issues[command.TargetID]
	if !exists {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if issue.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", issue.Version)
		return dataapi.CommandResult{}, false
	}
	if issue.Status == "resolved" || issue.Status == "known_nonblocking" || issue.Status == input.Status {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	admin, found := s.employees[actor]
	if !found || admin.Role != "admin" || !validUUID(admin.ID) {
		s.fail(w, requestID, http.StatusForbidden, "ADMIN_REQUIRED")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	previousStatus := issue.Status
	issue.Status = input.Status
	if input.Status == "in_progress" {
		assignedTo := admin.ID
		issue.AssignedTo = &assignedTo
		issue.BlocksIssuance = true
	} else {
		issue.BlocksIssuance = false
		comment := input.Comment
		resolvedBy := admin.ID
		issue.ResolutionComment = &comment
		issue.ResolvedBy = &resolvedBy
		issue.ResolvedAt = &now
	}
	issue.Version++
	issue.UpdatedAt = now
	s.issues[issue.ID] = issue
	s.issueActions = append(s.issueActions, issueActionAudit{
		IssueID: issue.ID, ActorEmployeeID: admin.ID, PreviousStatus: previousStatus,
		NextStatus: input.Status, Comment: input.Comment, CreatedAt: now,
	})
	if input.Status != "in_progress" {
		s.enqueueAdminNotification("issue_resolved", issue.ID, issue.VehicleID, now)
	}
	return commandResult("issue.resolve", issue), true
}

func validIssueActionAudit(audit issueActionAudit) bool {
	validStatus := func(status string) bool {
		return status == "open" || status == "in_progress" || status == "resolved" || status == "known_nonblocking"
	}
	commentValid := utf8.RuneCountInString(audit.Comment) <= 1000 &&
		(audit.NextStatus == "in_progress" || strings.TrimSpace(audit.Comment) != "")
	return validUUID(audit.IssueID) && validUUID(audit.ActorEmployeeID) && validStatus(audit.PreviousStatus) &&
		validStatus(audit.NextStatus) && audit.PreviousStatus != audit.NextStatus && commentValid && !audit.CreatedAt.IsZero()
}

func addIssueProjectionDefaults(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	addIssueProjectionFields(value)
	updated, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func addIssueProjectionFields(value any) {
	switch current := value.(type) {
	case map[string]any:
		if _, issue := current["author_id"]; issue {
			for _, field := range []string{"assigned_to", "resolution_comment", "resolved_by", "resolved_at"} {
				if _, exists := current[field]; !exists {
					current[field] = nil
				}
			}
		}
		for _, nested := range current {
			addIssueProjectionFields(nested)
		}
	case []any:
		for _, nested := range current {
			addIssueProjectionFields(nested)
		}
	}
}
