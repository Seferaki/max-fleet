package datamock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestPreviousInspectionPhotoServesOnlyLatestFinalizedVehiclePhoto(t *testing.T) {
	const (
		olderTripID        = "40000000-0000-4000-8000-000000000011"
		latestTripID       = "40000000-0000-4000-8000-000000000012"
		draftTripID        = "40000000-0000-4000-8000-000000000013"
		unknownVehicleID   = "10000000-0000-4000-8000-000000000099"
		olderInspectionID  = "50000000-0000-4000-8000-000000000011"
		latestInspectionID = "50000000-0000-4000-8000-000000000012"
		draftInspectionID  = "50000000-0000-4000-8000-000000000013"
		olderAssetID       = "60000000-0000-4000-8000-000000000011"
		latestAssetID      = "60000000-0000-4000-8000-000000000012"
		draftAssetID       = "60000000-0000-4000-8000-000000000013"
	)
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	snapshotPath := filepath.Join(t.TempDir(), "mock-state.json")
	mock, err := NewWithSnapshot("test-service-token", snapshotPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mock.assetDir, 0700); err != nil {
		t.Fatal(err)
	}
	photos := map[string][]byte{
		olderAssetID:  syntheticPNG(t, 11),
		latestAssetID: syntheticPNG(t, 22),
		draftAssetID:  syntheticPNG(t, 33),
	}
	for assetID, content := range photos {
		if err := os.WriteFile(filepath.Join(mock.assetDir, assetID), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	employeeID := mock.employees[driverID].ID
	for _, trip := range []dataapi.Trip{
		{ID: olderTripID, VehicleID: firstVehicleID, EmployeeID: employeeID, Status: "completed"},
		{ID: latestTripID, VehicleID: firstVehicleID, EmployeeID: mock.employees["8000000000000000002"].ID, Status: "completed"},
		{ID: draftTripID, VehicleID: firstVehicleID, EmployeeID: employeeID, Status: "returning"},
	} {
		mock.trips[trip.ID] = trip
	}
	inspection := func(id, status string, updatedAt time.Time) dataapi.Inspection {
		return dataapi.Inspection{ID: id, Phase: "after", Status: status, UpdatedAt: updatedAt}
	}
	mock.returns["20000000-0000-4000-8000-000000000011"] = dataapi.Return{Status: "completed", TripID: olderTripID, Inspection: inspection(olderInspectionID, "finalized", now)}
	mock.returns["20000000-0000-4000-8000-000000000012"] = dataapi.Return{Status: "completed", TripID: latestTripID, Inspection: inspection(latestInspectionID, "finalized", now.Add(time.Minute))}
	mock.returns["20000000-0000-4000-8000-000000000013"] = dataapi.Return{Status: "draft", TripID: draftTripID, Inspection: inspection(draftInspectionID, "draft", now.Add(time.Hour))}
	photo := func(assetID string, content []byte) photoRecord {
		return photoRecord{AssetID: assetID, SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), ContentType: "image/png"}
	}
	mock.photos[olderInspectionID] = map[int]photoRecord{2: photo(olderAssetID, photos[olderAssetID])}
	mock.photos[latestInspectionID] = map[int]photoRecord{2: photo(latestAssetID, photos[latestAssetID])}
	mock.photos[draftInspectionID] = map[int]photoRecord{2: photo(draftAssetID, photos[draftAssetID])}
	if err := mock.persist(); err != nil {
		t.Fatal(err)
	}

	client := commandClient(t, mock)
	ctx := context.Background()
	got, err := client.PreviousInspectionPhoto(ctx, "8000000000000000002", firstVehicleID, 2)
	if err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Bytes, photos[latestAssetID]) {
		t.Fatalf("another employee receives latest finalized image: %+v %v", got, err)
	}
	got, err = client.PreviousInspectionPhoto(ctx, "8000000000000000003", firstVehicleID, 2)
	if err != nil || !bytes.Equal(got.Bytes, photos[latestAssetID]) {
		t.Fatalf("admin receives latest finalized image: %+v %v", got, err)
	}
	for _, request := range []struct {
		actor, vehicle string
		slot           int
		wantCode       string
	}{
		{"8000000000000000009", firstVehicleID, 2, "ACCESS_DENIED"},
		{driverID, unknownVehicleID, 2, "NOT_FOUND"},
		{driverID, firstVehicleID, 1, "NOT_FOUND"},
	} {
		_, err := client.PreviousInspectionPhoto(ctx, request.actor, request.vehicle, request.slot)
		expectAPIError(t, err, request.wantCode)
	}
	for _, slot := range []int{0, 9} {
		if _, err := client.PreviousInspectionPhoto(ctx, driverID, firstVehicleID, slot); err == nil {
			t.Fatalf("client accepted invalid slot %d", slot)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/vehicles/"+firstVehicleID+"/previous-inspection/photos/2", nil)
	request.Header.Set("Authorization", "Bearer test-service-token")
	request.Header.Set("X-Contract-Version", dataapi.ContractVersion)
	request.Header.Set("X-Request-ID", "70000000-0000-4000-8000-000000000011")
	request.Header.Set("X-Actor-Max-ID", driverID)
	response := httptest.NewRecorder()
	mock.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || !bytes.Equal(response.Body.Bytes(), photos[latestAssetID]) {
		t.Fatalf("private photo response status=%d headers=%v body_bytes=%d", response.Code, response.Header(), response.Body.Len())
	}
	for _, privateID := range []string{olderTripID, latestTripID, latestInspectionID, latestAssetID, driverID} {
		if strings.Contains(response.Body.String(), privateID) {
			t.Fatalf("photo response exposed metadata identifier %s", privateID)
		}
	}
	request.URL.Path = "/internal/v1/vehicles/" + firstVehicleID + "/previous-inspection/photos/9"
	response = httptest.NewRecorder()
	mock.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid slot returned %d", response.Code)
	}

	restarted, err := NewWithSnapshot("test-service-token", snapshotPath, func() time.Time { return now.Add(time.Minute * 2) })
	if err != nil {
		t.Fatal(err)
	}
	client = commandClient(t, restarted)
	got, err = client.PreviousInspectionPhoto(ctx, driverID, firstVehicleID, 2)
	if err != nil || !bytes.Equal(got.Bytes, photos[latestAssetID]) {
		t.Fatalf("photo after mock restart: %+v %v", got, err)
	}
	if err := os.Remove(filepath.Join(restarted.assetDir, latestAssetID)); err != nil {
		t.Fatal(err)
	}
	_, err = client.PreviousInspectionPhoto(ctx, driverID, firstVehicleID, 2)
	expectAPIError(t, err, "STORAGE_UNAVAILABLE")
}
