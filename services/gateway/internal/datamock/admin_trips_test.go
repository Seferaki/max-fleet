package datamock

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestAdminTripsFilterPagingAndRole(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	driver := mock.employees[driverID].ID
	other := mock.employees["8000000000000000002"].ID
	for i := 1; i <= 4; i++ {
		owner, status := driver, "active"
		if i == 4 {
			owner = other
		}
		if i == 3 {
			status = "completed"
		}
		id := fmt.Sprintf("20000000-0000-4000-8000-%012d", i)
		mock.trips[id] = dataapi.Trip{ID: id, EmployeeID: owner, VehicleID: firstVehicleID, Status: status, StartedAt: now.Add(time.Duration(i) * time.Minute)}
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	_, err = client.AdminTrips(ctx, driverID, dataapi.AdminTripFilter{})
	expectAPIError(t, err, "ACCESS_DENIED")
	admin := "8000000000000000003"
	filter := dataapi.AdminTripFilter{State: "active", EmployeeID: driver, VehicleID: firstVehicleID, Limit: 1}
	first, err := client.AdminTrips(ctx, admin, filter)
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil || first.Items[0].ID != "20000000-0000-4000-8000-000000000002" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	filter.Cursor = *first.NextCursor
	second, err := client.AdminTrips(ctx, admin, filter)
	if err != nil || len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].ID != "20000000-0000-4000-8000-000000000001" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	filter.EmployeeID = other
	_, err = client.AdminTrips(ctx, admin, filter)
	expectAPIError(t, err, "INVALID_REQUEST")
	all, err := client.AdminTrips(ctx, admin, dataapi.AdminTripFilter{Limit: 10})
	if err != nil || len(all.Items) != 4 {
		t.Fatalf("all trips: %+v %v", all, err)
	}
}
