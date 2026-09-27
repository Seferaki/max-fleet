package datamock

import (
	"context"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestMyTripsPaginationAndActorIsolation(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		id := "10000000-0000-4000-8000-00000000000" + string(rune('0'+i))
		owner := mock.employees[driverID].ID
		if i == 4 {
			owner = mock.employees["8000000000000000002"].ID
		}
		mock.trips[id] = dataapi.Trip{ID: id, EmployeeID: owner, StartedAt: now.Add(time.Duration(i) * time.Minute), Status: "completed"}
	}
	client := commandClient(t, mock)
	first, err := client.MyTrips(context.Background(), driverID, 2, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].ID != "10000000-0000-4000-8000-000000000003" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := client.MyTrips(context.Background(), driverID, 2, *first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.NextCursor != nil {
		t.Fatalf("second page: %+v %v", second, err)
	}
	_, err = client.MyTrips(context.Background(), "8000000000000000002", 2, *first.NextCursor)
	expectAPIError(t, err, "INVALID_REQUEST")
	other, err := client.MyTrips(context.Background(), "8000000000000000002", 5, "")
	if err != nil || len(other.Items) != 1 || other.Items[0].ID != "10000000-0000-4000-8000-000000000004" {
		t.Fatalf("other actor: %+v %v", other, err)
	}
}
