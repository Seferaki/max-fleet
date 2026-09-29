package mapapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const testReturnID = "10000000-0000-4000-8000-000000000001"

type contextReader struct {
	me      dataapi.Me
	state   dataapi.CurrentState
	vehicle dataapi.Vehicle
	calls   int
}

func (f *contextReader) Me(context.Context, string) (dataapi.Me, error) {
	f.calls++
	return f.me, nil
}
func (f *contextReader) State(context.Context, string) (dataapi.CurrentState, error) {
	return f.state, nil
}
func (f *contextReader) Vehicle(context.Context, string, string) (dataapi.Vehicle, error) {
	return f.vehicle, nil
}

func TestContextRequiresSignedOwnerAndExplicitSelection(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	const token = "synthetic-map-test-token"
	verifier, err := NewVerifier(token, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tripID, employeeID, vehicleID := "20000000-0000-4000-8000-000000000001", "employee-1", "30000000-0000-4000-8000-000000000001"
	f := &contextReader{
		me: dataapi.Me{Allowed: true, MaxUserID: "123", Employee: &dataapi.Employee{ID: employeeID}},
		state: dataapi.CurrentState{
			Trip:   &dataapi.Trip{ID: tripID, EmployeeID: employeeID, VehicleID: vehicleID, ReturnID: &[]string{testReturnID}[0], Status: "returning"},
			Return: &dataapi.Return{ID: testReturnID, TripID: tripID, Status: "draft", Version: 2, IntentConfirmedAt: &now},
		},
		vehicle: dataapi.Vehicle{ID: vehicleID, CurrentParking: &dataapi.ParkingLocation{Latitude: 55.7, Longitude: 37.6}},
	}
	h, err := NewHandler(verifier, f, Point{Latitude: 55.75, Longitude: 37.62})
	if err != nil {
		t.Fatal(err)
	}
	raw := signedInitData(token, url.Values{"auth_date": {fmt.Sprint(now.Unix())}, "user": {`{"id":123}`}})
	serve := func(path, authorization string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.SetPathValue("id", testReturnID)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		h.Context(w, r)
		return w
	}
	if got := serve("/api/v1/returns/"+testReturnID+"/context", ""); got.Code != http.StatusUnauthorized || f.calls != 0 {
		t.Fatalf("missing auth: status=%d reader calls=%d", got.Code, f.calls)
	}
	if got := serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw+"&user=forged"); got.Code != http.StatusUnauthorized || f.calls != 0 {
		t.Fatalf("forged auth: status=%d reader calls=%d", got.Code, f.calls)
	}
	got := serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw)
	if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("context: status=%d body=%s", got.Code, got.Body.String())
	}
	var envelope struct {
		Data Context `json:"data"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Selected || envelope.Data.SelectedPoint != nil || envelope.Data.InitialCenter != (Point{55.7, 37.6}) {
		t.Fatalf("initial center became selected: %+v", envelope.Data)
	}
	f.state.Return.ParkingLocation = &dataapi.ParkingLocation{Latitude: 55.8, Longitude: 37.8, Source: "manual_map"}
	got = serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw)
	if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil || !envelope.Data.Selected || envelope.Data.SelectedPoint == nil || *envelope.Data.SelectedPoint != (Point{55.8, 37.8}) {
		t.Fatalf("saved selection not restored: %v %+v", err, envelope.Data)
	}
	f.state.Return.ID = "10000000-0000-4000-8000-000000000002"
	if got = serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw); got.Code != http.StatusNotFound {
		t.Fatalf("foreign or stale return: %d", got.Code)
	}
	f.state.Return.ID = testReturnID
	f.state.Trip.EmployeeID = "employee-2"
	if got = serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw); got.Code != http.StatusNotFound {
		t.Fatalf("foreign trip owner: %d", got.Code)
	}
	f.state.Trip.EmployeeID = employeeID
	f.state.Return.Status = "cancelled"
	if got = serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw); got.Code != http.StatusConflict {
		t.Fatalf("cancelled return: %d", got.Code)
	}
	f.state.Return.Status = "draft"
	f.state.Return.ParkingLocation = nil
	f.vehicle.CurrentParking = nil
	got = serve("/api/v1/returns/"+testReturnID+"/context", "MaxInitData "+raw)
	if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil || envelope.Data.Selected || envelope.Data.InitialCenter != (Point{55.75, 37.62}) {
		t.Fatalf("city fallback: status=%d err=%v context=%+v", got.Code, err, envelope.Data)
	}
}
