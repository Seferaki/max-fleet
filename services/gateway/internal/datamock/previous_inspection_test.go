package datamock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestPreviousInspectionOnlyLatestFinalizedAfter(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	_, err = client.PreviousInspection(context.Background(), driverID, firstVehicleID)
	expectAPIError(t, err, "NOT_FOUND")
	olderTrip := "10000000-0000-4000-8000-000000000001"
	newerTrip := "10000000-0000-4000-8000-000000000002"
	mock.trips[olderTrip] = dataapi.Trip{ID: olderTrip, VehicleID: firstVehicleID, EmployeeID: mock.employees[driverID].ID, Status: "completed"}
	mock.trips[newerTrip] = dataapi.Trip{ID: newerTrip, VehicleID: firstVehicleID, EmployeeID: mock.employees[driverID].ID, Status: "completed"}
	mock.returns["20000000-0000-4000-8000-000000000001"] = dataapi.Return{Status: "completed", TripID: olderTrip, Inspection: dataapi.Inspection{ID: "30000000-0000-4000-8000-000000000001", Phase: "after", Status: "finalized", UpdatedAt: now}}
	mock.returns["20000000-0000-4000-8000-000000000002"] = dataapi.Return{Status: "completed", TripID: newerTrip, Inspection: dataapi.Inspection{ID: "30000000-0000-4000-8000-000000000002", Phase: "after", Status: "finalized", UpdatedAt: now.Add(time.Minute)}}
	mock.returns["20000000-0000-4000-8000-000000000003"] = dataapi.Return{Status: "draft", TripID: newerTrip, Inspection: dataapi.Inspection{ID: "30000000-0000-4000-8000-000000000003", Phase: "after", Status: "draft", UpdatedAt: now.Add(time.Hour)}}
	previous, err := client.PreviousInspection(context.Background(), "8000000000000000002", firstVehicleID)
	if err != nil || previous.ID != "30000000-0000-4000-8000-000000000002" {
		t.Fatalf("latest previous inspection: %+v %v", previous, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/vehicles/"+firstVehicleID+"/previous-inspection", nil)
	request.Header.Set("Authorization", "Bearer test-service-token")
	request.Header.Set("X-Contract-Version", dataapi.ContractVersion)
	request.Header.Set("X-Request-ID", "40000000-0000-4000-8000-000000000001")
	request.Header.Set("X-Actor-Max-ID", "8000000000000000002")
	response := httptest.NewRecorder()
	mock.Handler().ServeHTTP(response, request)
	var envelope map[string]json.RawMessage
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || strings.Contains(response.Body.String(), olderTrip) || strings.Contains(response.Body.String(), driverID) {
		t.Fatal("previous inspection exposed trip or driver")
	}
}
