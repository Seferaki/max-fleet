package mapapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type Reader interface {
	Me(context.Context, string) (dataapi.Me, error)
	State(context.Context, string) (dataapi.CurrentState, error)
	Vehicle(context.Context, string, string) (dataapi.Vehicle, error)
}

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Context struct {
	ReturnID      string `json:"return_id"`
	ReturnVersion int64  `json:"return_version"`
	Selected      bool   `json:"selected"`
	SelectedPoint *Point `json:"selected_point"`
	InitialCenter Point  `json:"initial_center"`
}

type Handler struct {
	verifier *Verifier
	reader   Reader
	city     Point
}

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func NewHandler(verifier *Verifier, reader Reader, city Point) (*Handler, error) {
	if verifier == nil || reader == nil || !validPoint(city) {
		return nil, errors.New("map-api: invalid handler dependencies")
	}
	return &Handler{verifier: verifier, reader: reader, city: city}, nil
}

func (h *Handler) Context(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet || !uuidPattern.MatchString(r.PathValue("id")) || r.URL.RawQuery != "" {
		writeError(w, requestID, http.StatusBadRequest, "INVALID_REQUEST", "Неверный запрос", false)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "MaxInitData ") || len(r.Header.Values("Authorization")) != 1 {
		writeError(w, requestID, http.StatusUnauthorized, "INVALID_INIT_DATA", "Откройте карту снова из MAX", false)
		return
	}
	actor, err := h.verifier.Actor(strings.TrimPrefix(auth, "MaxInitData "))
	if err != nil {
		writeError(w, requestID, http.StatusUnauthorized, "INVALID_INIT_DATA", "Откройте карту снова из MAX", false)
		return
	}
	me, err := h.reader.Me(r.Context(), actor)
	if err != nil {
		writeUpstreamError(w, requestID, err)
		return
	}
	if !me.Allowed || me.Employee == nil || me.Employee.ID == "" || me.MaxUserID != actor {
		writeError(w, requestID, http.StatusNotFound, "NOT_FOUND", "Возврат недоступен", false)
		return
	}
	state, err := h.reader.State(r.Context(), actor)
	if err != nil {
		writeUpstreamError(w, requestID, err)
		return
	}
	if state.Trip == nil || state.Return == nil || state.Return.ID != r.PathValue("id") ||
		state.Trip.EmployeeID != me.Employee.ID || state.Return.TripID != state.Trip.ID ||
		state.Trip.ReturnID == nil || *state.Trip.ReturnID != state.Return.ID {
		writeError(w, requestID, http.StatusNotFound, "NOT_FOUND", "Возврат недоступен", false)
		return
	}
	if state.Trip.Status != "returning" || state.Return.Status != "draft" || state.Return.IntentConfirmedAt == nil || state.Return.Version < 1 {
		writeError(w, requestID, http.StatusConflict, "STALE_VERSION", "Состояние возврата изменилось", false)
		return
	}
	result := Context{ReturnID: state.Return.ID, ReturnVersion: state.Return.Version, InitialCenter: h.city}
	if state.Return.ParkingLocation != nil {
		point := Point{Latitude: state.Return.ParkingLocation.Latitude, Longitude: state.Return.ParkingLocation.Longitude}
		if validPoint(point) {
			result.Selected, result.SelectedPoint, result.InitialCenter = true, &point, point
		}
	} else {
		vehicle, err := h.reader.Vehicle(r.Context(), actor, state.Trip.VehicleID)
		if err == nil && vehicle.CurrentParking != nil {
			point := Point{Latitude: vehicle.CurrentParking.Latitude, Longitude: vehicle.CurrentParking.Longitude}
			if validPoint(point) {
				result.InitialCenter = point
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Data      Context `json:"data"`
		RequestID string  `json:"request_id"`
	}{result, requestID})
}

func validPoint(point Point) bool {
	return !math.IsNaN(point.Latitude) && !math.IsNaN(point.Longitude) &&
		point.Latitude >= -90 && point.Latitude <= 90 && point.Longitude >= -180 && point.Longitude <= 180
}

func writeUpstreamError(w http.ResponseWriter, requestID string, err error) {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden) {
		writeError(w, requestID, http.StatusNotFound, "NOT_FOUND", "Возврат недоступен", false)
		return
	}
	writeError(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE", "Сервис временно недоступен", true)
}

func writeError(w http.ResponseWriter, requestID string, status int, code, message string, retryable bool) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}{Error: struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}{code, message, retryable}, RequestID: requestID})
}

func newRequestID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return hex.EncodeToString(id[:4]) + "-" + hex.EncodeToString(id[4:6]) + "-" + hex.EncodeToString(id[6:8]) + "-" + hex.EncodeToString(id[8:10]) + "-" + hex.EncodeToString(id[10:])
}
