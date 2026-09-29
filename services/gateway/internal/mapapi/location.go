package mapapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type locationRequest struct {
	ExpectedVersion int64    `json:"expected_version"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
	Landmark        *string  `json:"landmark,omitempty"`
	Confirmed       bool     `json:"confirmed"`
}

type locationResponse struct {
	ReturnID      string `json:"return_id"`
	ReturnVersion int64  `json:"return_version"`
	Point         Point  `json:"point"`
	Source        string `json:"source"`
	Selected      bool   `json:"selected"`
}

func (h *Handler) Location(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost || !uuidPattern.MatchString(r.PathValue("id")) || r.URL.RawQuery != "" || len(r.Header.Values("Idempotency-Key")) != 1 {
		writeError(w, requestID, http.StatusBadRequest, "INVALID_REQUEST", "Неверный запрос", false)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 200 || strings.ContainsAny(key, "\r\n/\\?#") {
		writeError(w, requestID, http.StatusBadRequest, "INVALID_REQUEST", "Неверный ключ повтора", false)
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeError(w, requestID, http.StatusUnauthorized, "INVALID_INIT_DATA", "Откройте карту снова из MAX", false)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, requestID, http.StatusBadRequest, "INVALID_REQUEST", "Ожидается JSON", false)
		return
	}
	var input locationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || input.ExpectedVersion < 1 ||
		input.Latitude == nil || input.Longitude == nil || !input.Confirmed ||
		!validPoint(Point{*input.Latitude, *input.Longitude}) || input.Landmark != nil && len(*input.Landmark) > 500 {
		writeError(w, requestID, http.StatusBadRequest, "INVALID_REQUEST", "Выберите и подтвердите точку", false)
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
	if state.Trip.Status != "returning" || state.Return.Status != "draft" || state.Return.IntentConfirmedAt == nil {
		writeError(w, requestID, http.StatusConflict, "STALE_VERSION", "Состояние возврата изменилось", false)
		return
	}
	previous, err := h.reader.OwnCommandResult(r.Context(), actor, key, "return.set_location")
	if err == nil {
		if state.Return.Version != input.ExpectedVersion+1 || state.Return.ParkingLocation == nil ||
			state.Return.ParkingLocation.Source != "manual_map" ||
			state.Return.ParkingLocation.Latitude != *input.Latitude || state.Return.ParkingLocation.Longitude != *input.Longitude ||
			!sameLandmark(state.Return.ParkingLocation.Landmark, input.Landmark) {
			writeError(w, requestID, http.StatusConflict, "STALE_VERSION", "Точка возврата уже изменилась", false)
			return
		}
		h.writeLocationResult(w, requestID, r.PathValue("id"), input, previous)
		return
	}
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		writeUpstreamError(w, requestID, err)
		return
	}
	if state.Return.Version != input.ExpectedVersion {
		writeError(w, requestID, http.StatusConflict, "STALE_VERSION", "Состояние возврата изменилось", false)
		return
	}
	result, err := h.reader.ReturnSetLocation(r.Context(), actor, state.Return.ID, input.ExpectedVersion, key, nil,
		dataapi.LocationInput{Latitude: *input.Latitude, Longitude: *input.Longitude, Landmark: input.Landmark, Source: "manual_map", Confirmed: true})
	if err != nil {
		if errors.As(err, &apiErr) {
			if apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden {
				writeError(w, requestID, http.StatusNotFound, "NOT_FOUND", "Возврат недоступен", false)
				return
			}
			if apiErr.Status == http.StatusConflict {
				writeError(w, requestID, http.StatusConflict, "STALE_VERSION", "Состояние возврата изменилось", false)
				return
			}
		}
		writeUpstreamError(w, requestID, err)
		return
	}
	h.writeLocationResult(w, requestID, r.PathValue("id"), input, result)
}

func (h *Handler) writeLocationResult(w http.ResponseWriter, requestID, returnID string, input locationRequest, result dataapi.CommandResult) {
	draft, err := dataapi.DecodeAggregate[dataapi.Return](result)
	if err != nil || result.Operation != "return.set_location" || draft.ID != returnID || draft.Version != input.ExpectedVersion+1 ||
		draft.ParkingLocation == nil || draft.ParkingLocation.Source != "manual_map" ||
		draft.ParkingLocation.Latitude != *input.Latitude || draft.ParkingLocation.Longitude != *input.Longitude ||
		!sameLandmark(draft.ParkingLocation.Landmark, input.Landmark) {
		writeError(w, requestID, http.StatusConflict, "STALE_VERSION", "Ключ повтора уже использован для другой точки", false)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Data      locationResponse `json:"data"`
		RequestID string           `json:"request_id"`
	}{locationResponse{draft.ID, draft.Version, Point{*input.Latitude, *input.Longitude}, "manual_map", true}, requestID})
}

func sameLandmark(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
