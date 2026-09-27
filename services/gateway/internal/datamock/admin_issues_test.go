package datamock

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestAdminIssuesFilterPagingAndRole(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		status, vehicle := "open", firstVehicleID
		if i == 3 {
			status = "resolved"
		}
		if i == 4 {
			vehicle = "10000000-0000-4000-8000-000000000002"
		}
		id := fmt.Sprintf("20000000-0000-4000-8000-%012d", i)
		mock.issues[id] = dataapi.Issue{ID: id, VehicleID: vehicle, Status: status, UpdatedAt: now.Add(time.Duration(i) * time.Minute)}
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	_, err = client.AdminIssues(ctx, driverID, dataapi.AdminIssueFilter{})
	expectAPIError(t, err, "ACCESS_DENIED")
	admin := "8000000000000000003"
	filter := dataapi.AdminIssueFilter{Status: "open", VehicleID: firstVehicleID, Limit: 1}
	first, err := client.AdminIssues(ctx, admin, filter)
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil || first.Items[0].ID != "20000000-0000-4000-8000-000000000002" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	filter.Cursor = *first.NextCursor
	second, err := client.AdminIssues(ctx, admin, filter)
	if err != nil || len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].ID != "20000000-0000-4000-8000-000000000001" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	filter.Status = "resolved"
	_, err = client.AdminIssues(ctx, admin, filter)
	expectAPIError(t, err, "INVALID_REQUEST")
	all, err := client.AdminIssues(ctx, admin, dataapi.AdminIssueFilter{Limit: 10})
	if err != nil || len(all.Items) != 4 {
		t.Fatalf("all issues: %+v %v", all, err)
	}
}
