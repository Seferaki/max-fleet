package datamock

import (
	"sort"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

// projectTrip adds linked reports without changing the finalized trip snapshot.
// Call while holding s.mu.
func (s *Server) projectTrip(trip dataapi.Trip) dataapi.Trip {
	if trip.Status != "completed" {
		return trip
	}
	linked := make([]dataapi.Issue, 0)
	for _, issue := range s.issues {
		if issue.Stage == "post_return" && issue.TripID != nil && *issue.TripID == trip.ID {
			linked = append(linked, issue)
		}
	}
	if len(linked) == 0 {
		return trip
	}
	sort.Slice(linked, func(i, j int) bool {
		if linked[i].UpdatedAt.Equal(linked[j].UpdatedAt) {
			return linked[i].ID < linked[j].ID
		}
		return linked[i].UpdatedAt.Before(linked[j].UpdatedAt)
	})
	trip.Issues = append(append([]dataapi.Issue{}, trip.Issues...), linked...)
	return trip
}
