package mapapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestLocationRequiresSelectedOwnerVersionAndRecoversRetry(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	const token = "synthetic-map-test-token"
	verifier, _ := NewVerifier(token, func() time.Time { return now })
	tripID, employeeID := "20000000-0000-4000-8000-000000000001", "employee-1"
	f := &contextReader{
		me: dataapi.Me{Allowed: true, MaxUserID: "123", Employee: &dataapi.Employee{ID: employeeID}},
		state: dataapi.CurrentState{
			Trip:   &dataapi.Trip{ID: tripID, EmployeeID: employeeID, ReturnID: &[]string{testReturnID}[0], Status: "returning"},
			Return: &dataapi.Return{ID: testReturnID, TripID: tripID, Status: "draft", Version: 2, IntentConfirmedAt: &now},
		},
	}
	h, _ := NewHandler(verifier, f, Point{55.75, 37.62})
	raw := signedInitData(token, url.Values{"auth_date": {fmt.Sprint(now.Unix())}, "user": {`{"id":123}`}})
	validBody := `{"expected_version":2,"latitude":55.7,"longitude":37.6,"confirmed":true}`
	send := func(body, auth, key string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/returns/"+testReturnID+"/location", strings.NewReader(body))
		r.SetPathValue("id", testReturnID)
		r.Header.Set("Authorization", auth)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Location(w, r)
		return w.Code
	}
	for _, body := range []string{
		`{"expected_version":2,"latitude":55.7,"longitude":37.6,"confirmed":false}`,
		`{"expected_version":2,"latitude":55.7,"longitude":37.6}`,
		`{"expected_version":2,"latitude":91,"longitude":37.6,"confirmed":true}`,
		`{"expected_version":2,"longitude":37.6,"confirmed":true}`,
	} {
		if got := send(body, "MaxInitData "+raw, "stable-key-1"); got != http.StatusBadRequest || f.writes != 0 {
			t.Fatalf("invalid selection: status=%d writes=%d", got, f.writes)
		}
	}
	if got := send(validBody, "MaxInitData "+raw+"&user=forged", "stable-key-1"); got != http.StatusUnauthorized || f.writes != 0 {
		t.Fatalf("forged signature: status=%d writes=%d", got, f.writes)
	}
	f.state.Trip.EmployeeID = "foreign"
	if got := send(validBody, "MaxInitData "+raw, "stable-key-1"); got != http.StatusNotFound || f.writes != 0 {
		t.Fatalf("foreign trip: status=%d writes=%d", got, f.writes)
	}
	f.state.Trip.EmployeeID = employeeID
	if got := send(strings.Replace(validBody, `"expected_version":2`, `"expected_version":1`, 1), "MaxInitData "+raw, "stable-key-1"); got != http.StatusConflict || f.writes != 0 {
		t.Fatalf("stale version: status=%d writes=%d", got, f.writes)
	}
	if got := send(validBody, "MaxInitData "+raw, "stable-key-1"); got != http.StatusOK || f.writes != 1 || f.state.Return.Version != 3 {
		t.Fatalf("first save: status=%d writes=%d version=%d", got, f.writes, f.state.Return.Version)
	}
	if got := send(validBody, "MaxInitData "+raw, "stable-key-1"); got != http.StatusOK || f.writes != 1 {
		t.Fatalf("retry after lost reply: status=%d writes=%d", got, f.writes)
	}
	changed := strings.Replace(validBody, `"latitude":55.7`, `"latitude":55.8`, 1)
	if got := send(changed, "MaxInitData "+raw, "stable-key-1"); got != http.StatusConflict || f.writes != 1 {
		t.Fatalf("key reused with another point: status=%d writes=%d", got, f.writes)
	}
	f.state.Return.Version = 4
	f.state.Return.ParkingLocation.Latitude = 55.9
	if got := send(validBody, "MaxInitData "+raw, "stable-key-1"); got != http.StatusConflict || f.writes != 1 {
		t.Fatalf("old retry after another location: status=%d writes=%d", got, f.writes)
	}
}
