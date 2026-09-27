package datamock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const driverID = "8000000000000000001"

func TestClientReadsSyntheticFixtures(t *testing.T) {
	mock, err := New("test-service-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	readiness, err := http.Get(server.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	readiness.Body.Close()
	if readiness.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("partial mock reported ready: %d", readiness.StatusCode)
	}
	client, err := dataapi.New(dataapi.Config{BaseURL: server.URL + "/internal/v1", Token: "test-service-token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	meta, err := client.Meta(ctx)
	if err != nil || meta.Mode != "mock" || meta.ContractVersion != "1.0" {
		t.Fatalf("meta: %+v %v", meta, err)
	}
	me, err := client.Me(ctx, driverID)
	if err != nil || !me.Allowed || me.Employee == nil || me.Employee.Role != "employee" {
		t.Fatalf("me: %+v %v", me, err)
	}
	rules, err := client.CurrentRules(ctx, driverID)
	if err != nil || rules.ID != "90000000-0000-4000-8000-000000000001" || rules.Body == "" {
		t.Fatalf("current rules: %+v %v", rules, err)
	}
	unknown, err := client.Me(ctx, "900001")
	if err != nil || unknown.Allowed || unknown.Employee != nil || unknown.MaxUserID != "900001" {
		t.Fatalf("unknown: %+v %v", unknown, err)
	}
	page, err := client.Vehicles(ctx, driverID, dataapi.VehicleFilter{})
	if err != nil || len(page.Items) != 5 || page.NextCursor == nil {
		t.Fatalf("first page: %+v %v", page, err)
	}
	second, err := client.Vehicles(ctx, driverID, dataapi.VehicleFilter{Cursor: *page.NextCursor})
	if err != nil || len(second.Items) != 5 || second.NextCursor != nil {
		t.Fatalf("second page: %+v %v", second, err)
	}
	if _, err := client.Vehicles(ctx, driverID, dataapi.VehicleFilter{Cursor: *page.NextCursor, Limit: 1}); err == nil {
		t.Fatal("cursor accepted with a different limit")
	}
	vehicle, err := client.Vehicle(ctx, driverID, page.Items[0].ID)
	if err != nil || vehicle.Plate != "DEMO-001" || vehicle.CurrentFuel == nil || *vehicle.CurrentFuel != 100 {
		t.Fatalf("vehicle: %+v %v", vehicle, err)
	}
}

func TestMockAuthorizationAndVersion(t *testing.T) {
	mock, err := New("test-service-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	client, err := dataapi.New(dataapi.Config{BaseURL: server.URL + "/internal/v1", Token: "test-service-token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Vehicles(context.Background(), "900001", dataapi.VehicleFilter{})
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != "ACCESS_DENIED" {
		t.Fatalf("unknown actor: %v", err)
	}
	_, err = client.State(context.Background(), "900001")
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("unknown actor state: %v", err)
	}
	_, err = client.CurrentRules(context.Background(), "900001")
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("unknown actor rules: %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/internal/v1/vehicles", nil)
	request.Header.Set("Authorization", "Bearer wrong-token")
	request.Header.Set("X-Contract-Version", "1.0")
	request.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	request.Header.Set("X-Actor-Max-ID", driverID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status %d", response.StatusCode)
	}
	request.Header.Set("Authorization", "Bearer test-service-token")
	request.Header.Set("X-Contract-Version", "2.0")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong version status %d", response.StatusCode)
	}
}
