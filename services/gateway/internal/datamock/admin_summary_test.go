package datamock

import (
	"context"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestAdminSummaryCountsAndRole(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	_, err = client.AdminSummary(ctx, driverID)
	expectAPIError(t, err, "ACCESS_DENIED")
	admin := "8000000000000000003"
	initial, err := client.AdminSummary(ctx, admin)
	if err != nil || initial.Available != 10 || initial.Holding != 0 || initial.OpenIssues != 0 {
		t.Fatalf("initial: %+v %v", initial, err)
	}
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "summary-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	holding, err := client.AdminSummary(ctx, admin)
	if err != nil || holding.Available != 9 || holding.Holding != 1 {
		t.Fatalf("holding: %+v %v", holding, err)
	}
	_, err = client.IssueCreate(ctx, driverID, firstVehicleID, 2, dataapi.IssueCreateInput{Category: "body_damage", Description: "Синтетическое замечание", InspectionID: &hold.Inspection.ID}, "summary-issue", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := client.AdminSummary(ctx, admin)
	if err != nil || issue.Available != 9 || issue.Holding != 0 || issue.NeedsReview != 1 || issue.OpenIssues != 1 {
		t.Fatalf("issue: %+v %v", issue, err)
	}
}
