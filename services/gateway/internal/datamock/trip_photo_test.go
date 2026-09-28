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
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestTripInspectionPhotoAccessPhaseRestartAndStorage(t *testing.T) {
	const tripID = "40000000-0000-4000-8000-000000000001"
	const beforeID = "50000000-0000-4000-8000-000000000001"
	const afterID = "50000000-0000-4000-8000-000000000002"
	const beforeAsset = "60000000-0000-4000-8000-000000000001"
	const afterAsset = "60000000-0000-4000-8000-000000000002"
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshot("test-service-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mock.assetDir, 0700); err != nil {
		t.Fatal(err)
	}
	beforeBytes, afterBytes := syntheticPNG(t, 10), syntheticPNG(t, 20)
	for id, data := range map[string][]byte{beforeAsset: beforeBytes, afterAsset: afterBytes} {
		if err := os.WriteFile(filepath.Join(mock.assetDir, id), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := dataapi.Inspection{ID: beforeID, Phase: "before", Status: "finalized"}
	after := dataapi.Inspection{ID: afterID, Phase: "after", Status: "finalized"}
	mock.trips[tripID] = dataapi.Trip{ID: tripID, EmployeeID: mock.employees[driverID].ID, Status: "active", BeforeInspection: before, AfterInspection: &after}
	mock.photos[beforeID] = map[int]photoRecord{3: {AssetID: beforeAsset, SHA256: fmt.Sprintf("%x", sha256.Sum256(beforeBytes)), ContentType: "image/png"}}
	mock.photos[afterID] = map[int]photoRecord{3: {AssetID: afterAsset, SHA256: fmt.Sprintf("%x", sha256.Sum256(afterBytes)), ContentType: "image/png"}}
	if err := mock.persist(); err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	got, err := client.TripInspectionPhoto(ctx, driverID, tripID, "before", 3)
	if err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Bytes, beforeBytes) {
		t.Fatalf("owner before: %+v %v", got, err)
	}
	got, err = client.TripInspectionPhoto(ctx, "8000000000000000003", tripID, "before", 3)
	if err != nil || !bytes.Equal(got.Bytes, beforeBytes) {
		t.Fatalf("admin before: %+v %v", got, err)
	}
	for _, item := range []struct {
		actor, phase string
		slot         int
	}{
		{"8000000000000000002", "before", 3},
		{driverID, "before", 4},
		{driverID, "after", 3},
	} {
		_, err := client.TripInspectionPhoto(ctx, item.actor, tripID, item.phase, item.slot)
		expectAPIError(t, err, "NOT_FOUND")
	}
	for _, item := range []struct {
		phase string
		slot  int
	}{{"draft", 3}, {"before", 0}, {"after", 9}} {
		if _, err := client.TripInspectionPhoto(ctx, driverID, tripID, item.phase, item.slot); err == nil {
			t.Fatalf("accepted invalid phase/slot: %+v", item)
		}
	}
	trip := mock.trips[tripID]
	trip.Status = "completed"
	mock.trips[tripID] = trip
	if err := mock.persist(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client = commandClient(t, restarted)
	got, err = client.TripInspectionPhoto(ctx, "8000000000000000003", tripID, "after", 3)
	if err != nil || !bytes.Equal(got.Bytes, afterBytes) {
		t.Fatalf("admin after restart: %+v %v", got, err)
	}
	closed := restarted.trips[tripID]
	closed.Status = "closed_by_admin"
	restarted.trips[tripID] = closed
	got, err = client.TripInspectionPhoto(ctx, driverID, tripID, "after", 3)
	if err != nil || !bytes.Equal(got.Bytes, afterBytes) {
		t.Fatalf("finalized after on admin-closed trip: %+v %v", got, err)
	}
	if err := os.Remove(filepath.Join(restarted.assetDir, afterAsset)); err != nil {
		t.Fatal(err)
	}
	_, err = client.TripInspectionPhoto(ctx, driverID, tripID, "after", 3)
	expectAPIError(t, err, "STORAGE_UNAVAILABLE")

	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	request := httptest.NewRequest(http.MethodGet, server.URL+"/internal/v1/trips/"+tripID+"/inspection-photos/before/3", nil)
	request.Header.Set("Authorization", "Bearer test-service-token")
	request.Header.Set("X-Request-ID", "70000000-0000-4000-8000-000000000001")
	request.Header.Set("X-Actor-Max-ID", driverID)
	request.Header.Set("X-Contract-Version", "1.0")
	response := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("old contract version accepted: %d", response.Code)
	}
	request.Header.Set("X-Contract-Version", dataapi.ContractVersion)
	request.Header.Set("Authorization", "Bearer wrong-token")
	response = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong service token accepted: %d", response.Code)
	}
}
