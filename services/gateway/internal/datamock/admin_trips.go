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

func (s *Server) adminTrips(w http.ResponseWriter, r *http.Request, requestID string) {
	query := r.URL.Query()
	for key, values := range query {
		if (key != "state" && key != "employee_id" && key != "vehicle_id" && key != "limit" && key != "cursor") || len(values) != 1 {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	state, employeeID, vehicleID := query.Get("state"), query.Get("employee_id"), query.Get("vehicle_id")
	if state != "" && state != "active" && state != "returning" && state != "completed" && state != "closed_by_admin" || query.Has("employee_id") && !validUUID(employeeID) || query.Has("vehicle_id") && !validUUID(vehicleID) {
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
	items := make([]dataapi.Trip, 0)
	for _, trip := range s.trips {
		if state != "" && trip.Status != state || employeeID != "" && trip.EmployeeID != employeeID || vehicleID != "" && trip.VehicleID != vehicleID {
			continue
		}
		items = append(items, s.projectTrip(trip))
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
		if err != nil || len(parts) != 5 || parts[1] != strconv.Itoa(limit) || parts[2] != state || parts[3] != employeeID || parts[4] != vehicleID {
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
		encoded := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d:%s:%s:%s", end, limit, state, employeeID, vehicleID)))
		next = &encoded
	}
	s.success(w, requestID, dataapi.Page[dataapi.Trip]{Items: items[offset:end], NextCursor: next})
}
