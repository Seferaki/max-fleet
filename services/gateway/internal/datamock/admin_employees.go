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

func (s *Server) adminEmployees(w http.ResponseWriter, r *http.Request, requestID string) {
	query := r.URL.Query()
	for key := range query {
		if key != "limit" && key != "cursor" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
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
	if s.employees[r.Header.Get("X-Actor-Max-ID")].Role != "admin" {
		s.fail(w, requestID, http.StatusForbidden, "ACCESS_DENIED")
		return
	}
	items := make([]dataapi.Employee, 0, len(s.employees))
	for _, employee := range s.employees {
		items = append(items, employee)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	offset := 0
	if query.Has("cursor") {
		decoded, err := base64.RawURLEncoding.DecodeString(query.Get("cursor"))
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 2 || parts[1] != strconv.Itoa(limit) {
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
		encoded := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d", end, limit)))
		next = &encoded
	}
	s.success(w, requestID, dataapi.Page[dataapi.Employee]{Items: items[offset:end], NextCursor: next})
}

func (s *Server) adminEmployee(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.employees[r.Header.Get("X-Actor-Max-ID")].Role != "admin" {
		s.fail(w, requestID, http.StatusForbidden, "ACCESS_DENIED")
		return
	}
	for _, employee := range s.employees {
		if employee.ID == id {
			s.success(w, requestID, employee)
			return
		}
	}
	s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
}
