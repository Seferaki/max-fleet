package datamock

import (
	"net/http"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) adminSummary(w http.ResponseWriter, r *http.Request, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.employees[r.Header.Get("X-Actor-Max-ID")].Role != "admin" {
		s.fail(w, requestID, http.StatusForbidden, "ACCESS_DENIED")
		return
	}
	if !s.expireAndSave(w, requestID) {
		return
	}
	var summary dataapi.AdminSummary
	for _, vehicle := range s.vehicles {
		if vehicle.Status == "available" {
			summary.Available++
		}
		if vehicle.NeedsReview {
			summary.NeedsReview++
		}
	}
	for _, checkout := range s.checkouts {
		if checkout.Status == "holding" {
			summary.Holding++
		}
	}
	for _, trip := range s.trips {
		if trip.Status == "active" {
			summary.ActiveTrips++
		}
		if trip.Status == "returning" {
			summary.Returning++
		}
	}
	for _, issue := range s.issues {
		if issue.Status == "open" {
			summary.OpenIssues++
		}
	}
	s.success(w, requestID, summary)
}
