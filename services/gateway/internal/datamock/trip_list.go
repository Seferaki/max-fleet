package datamock

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func (s *Server) myTrips(w http.ResponseWriter, r *http.Request, requestID string) {
	query := r.URL.Query()
	for key := range query {
		if key != "scope" && key != "limit" && key != "cursor" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	if query.Get("scope") != "mine" || len(query["scope"]) != 1 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	limit := 5
	if query.Has("limit") {
		if len(query["limit"]) != 1 {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		value, err := strconv.Atoi(query.Get("limit"))
		if err != nil || value < 1 || value > 50 {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		limit = value
	}
	if len(query["cursor"]) > 1 || len(query.Get("cursor")) > 2048 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.employees[r.Header.Get("X-Actor-Max-ID")].ID
	items := make([]dataapi.Trip, 0)
	for _, trip := range s.trips {
		if trip.EmployeeID == owner {
			items = append(items, s.projectTrip(trip))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].StartedAt.After(items[j].StartedAt)
	})
	offset := 0
	if query.Has("cursor") {
		decoded, err := base64.RawURLEncoding.DecodeString(query.Get("cursor"))
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 3 || parts[0] != owner || parts[2] != strconv.Itoa(limit) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 1 || offset >= len(items) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	end := min(offset+limit, len(items))
	var next *string
	if end < len(items) {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d:%d", owner, end, limit)))
		next = &cursor
	}
	s.success(w, requestID, dataapi.Page[dataapi.Trip]{Items: items[offset:end], NextCursor: next})
}
