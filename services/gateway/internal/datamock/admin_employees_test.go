package datamock

import (
	"context"
	"testing"
	"time"
)

func TestAdminEmployeesPagingAndRole(t *testing.T) {
	mock, err := NewWithClock("test-service-token", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	_, err = client.AdminEmployees(ctx, driverID, 2, "")
	expectAPIError(t, err, "ACCESS_DENIED")
	admin := "8000000000000000003"
	first, err := client.AdminEmployees(ctx, admin, 2, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := client.AdminEmployees(ctx, admin, 2, *first.NextCursor)
	if err != nil || len(second.Items) != 2 || second.NextCursor != nil || first.Items[0].ID == second.Items[0].ID {
		t.Fatalf("second page: %+v %v", second, err)
	}
	_, err = client.AdminEmployees(ctx, admin, 3, *first.NextCursor)
	expectAPIError(t, err, "INVALID_REQUEST")
	employee, err := client.AdminEmployee(ctx, admin, first.Items[0].ID)
	if err != nil || employee.ID != first.Items[0].ID {
		t.Fatalf("employee: %+v %v", employee, err)
	}
	_, err = client.AdminEmployee(ctx, driverID, first.Items[0].ID)
	expectAPIError(t, err, "ACCESS_DENIED")
	_, err = client.AdminEmployee(ctx, admin, "70000000-0000-4000-8000-000000000001")
	expectAPIError(t, err, "NOT_FOUND")
}
