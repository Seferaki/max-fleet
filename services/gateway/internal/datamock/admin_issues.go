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

func (s *Server) adminIssues(w http.ResponseWriter, r *http.Request, requestID string) {
	query := r.URL.Query()
	for key, values := range query {
		if (key != "status" && key != "vehicle_id" && key != "limit" && key != "cursor") || len(values) != 1 {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	status, vehicleID := query.Get("status"), query.Get("vehicle_id")
	if status != "" && status != "open" && status != "in_progress" && status != "resolved" && status != "known_nonblocking" || query.Has("vehicle_id") && !validUUID(vehicleID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	limit := 5
	if query.Has("limit") {
		value, err := strconv.Atoi(query.Get("limit"))
		if err != nil || value < 1 || value > 50 {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		limit = value
	}
	if len(query.Get("cursor")) > 2048 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.employees[r.Header.Get("X-Actor-Max-ID")].Role != "admin" {
		s.fail(w, requestID, http.StatusForbidden, "ACCESS_DENIED")
		return
	}
	items := make([]dataapi.Issue, 0)
	for _, issue := range s.issues {
		if status != "" && issue.Status != status || vehicleID != "" && issue.VehicleID != vehicleID {
			continue
		}
		items = append(items, issue)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	offset := 0
	if query.Has("cursor") {
		decoded, err := base64.RawURLEncoding.DecodeString(query.Get("cursor"))
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 4 || parts[1] != strconv.Itoa(limit) || parts[2] != status || parts[3] != vehicleID {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		offset, err = strconv.Atoi(parts[0])
		if err != nil || offset < 1 || offset >= len(items) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	end := min(offset+limit, len(items))
	var next *string
	if end < len(items) {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d:%s:%s", end, limit, status, vehicleID)))
		next = &encoded
	}
	s.success(w, requestID, dataapi.Page[dataapi.Issue]{Items: items[offset:end], NextCursor: next})
}
