package dialog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestReturnCompleteRequiresSafetyAndRecoversLostReply(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	readyChecklistDraft(t, actor, driver)
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Trip == nil {
		t.Fatal(err)
	}
	returnID, tripID, inspectionID := state.Return.ID, state.Trip.ID, state.Return.Inspection.ID
	noDamage, clean, safe := false, true, true
	fuel, odometer := 75, int64(12020)
	input := dataapi.InspectionUpdateInput{NewDamage: &noDamage, CabinClean: &clean, ParkingAllowed: &safe, KeysReturned: &safe, CarLocked: &safe, FuelLevel: &fuel, OdometerKM: &odometer}
	result, err := actor.InspectionUpdate(ctx, driver, inspectionID, state.Return.Inspection.Version, input, "complete-setup-fields", nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil {
		t.Fatal(err)
	}
	for slot := 1; slot <= 8; slot++ {
		key := fmt.Sprintf("complete-setup-photo-%d", slot)
		uploaded, err := actor.UploadInspectionPhoto(ctx, driver, dataapi.InspectionPhotoInput{InspectionID: inspectionID, Slot: slot, Version: inspection.Version, SourceEventKey: key, IdempotencyKey: key, ContentType: "image/png", Image: samplePhoto(t, uint8(110+slot))})
		if err != nil {
			t.Fatalf("after photo %d: %v", slot, err)
		}
		inspection = uploaded.Inspection
	}
	if _, err := actor.InspectionConfirmPhotos(ctx, driver, inspectionID, inspection.Version, "complete-setup-confirm-photos", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil {
		t.Fatal(err)
	}
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(ctx, callbackItem(driver, "complete-missing-location", fmt.Sprintf("return-summary:%s:%d", returnID, state.Return.Version), now)); err != nil || !strings.Contains(sender.Messages()[0].Text, "подтверждённая точка парковки") {
		t.Fatalf("missing location summary: %v %+v", err, sender.Messages())
	}
	if _, err := actor.ReturnSetLocation(ctx, driver, returnID, state.Return.Version, "complete-setup-location", nil, dataapi.LocationInput{Latitude: 55.75, Longitude: 37.62, Source: "manual_map", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil {
		t.Fatal(err)
	}
	if err := processor.Handle(ctx, menuItem(driver, "complete-menu", now)); err != nil {
		t.Fatal(err)
	}
	var summary string
	for _, row := range sender.Messages()[1].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-summary:") {
			summary = row[0].Payload
		}
	}
	if summary == "" {
		t.Fatal("return summary missing")
	}
	if err := processor.Handle(ctx, callbackItem(driver, "complete-review", summary, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "Фото после: 8/8") || !strings.Contains(sender.Messages()[2].Text, "Нажимая") {
		t.Fatalf("return summary: %v %+v", err, sender.Messages())
	}
	complete := sender.Messages()[2].Buttons[0][0].Payload
	foreign := callbackItem("8000000000000000002", "complete-foreign", complete, now)
	foreign.LeaseToken = "synthetic-test-lease"
	if err := processor.Handle(ctx, foreign); err != nil || !strings.Contains(sender.Messages()[3].Text, "изменился") {
		t.Fatalf("foreign complete: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "complete-no-lease", complete, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("complete without lease: %v", err)
	}
	event := callbackItem(driver, "complete-submit", complete, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "complete-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost completion reply: %+v %v", result, err)
	}
	trip, err := actor.Trip(ctx, driver, tripID)
	if err != nil || trip.Status != "completed" || trip.ParkingLocation == nil || trip.AfterInspection == nil || len(trip.AfterInspection.OccupiedSlots) != 8 {
		t.Fatalf("committed trip: %+v %v", trip, err)
	}
	vehicle, err := actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("vehicle not released after commit: %+v %v", vehicle, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Возврат подтверждён") {
		t.Fatalf("completion reply recovery: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return != nil || state.Trip != nil {
		t.Fatalf("completed return still active: %+v %v", state, err)
	}
}

func TestReturnSummaryBlocksUnsafeParkingKeysAndLock(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	draft := readyChecklistDraft(t, actor, driver)
	unsafe := false
	if _, err := actor.InspectionUpdate(ctx, driver, draft.Inspection.ID, draft.Inspection.Version, dataapi.InspectionUpdateInput{ParkingAllowed: &unsafe, KeysReturned: &unsafe, CarLocked: &unsafe}, "unsafe-complete-setup", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil {
		t.Fatal(err)
	}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	payload := fmt.Sprintf("return-summary:%s:%d", draft.ID, state.Return.Version)
	if err := processor.Handle(ctx, callbackItem(driver, "unsafe-summary", payload, now)); err != nil || !strings.Contains(sender.Messages()[0].Text, "Самостоятельное завершение возврата небезопасно") || len(sender.Messages()[0].Buttons) != 1 || sender.Messages()[0].Buttons[0][0].Text != "Сообщить проблему" {
		t.Fatalf("unsafe summary: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil || state.Trip.Status != "returning" || state.Return == nil || state.Return.Status != "draft" {
		t.Fatalf("unsafe summary released trip: %+v %v", state, err)
	}
}

func TestReturnMenuFitsMAXKeyboardWithAdminIssueDraft(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	tripID, returnID, inspectionID := "10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000001"
	vehicleID, employeeID := "40000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000001"
	noDamage, clean, safe := false, true, true
	fuel, odometer := 75, int64(12020)
	category, description := "mechanical", "Проблема"
	state := dataapi.CurrentState{
		Trip:         &dataapi.Trip{ID: tripID, VehicleID: vehicleID, EmployeeID: employeeID, Status: "returning", ReturnID: &returnID},
		Return:       &dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", Step: "checklist", Inspection: dataapi.Inspection{ID: inspectionID, Phase: "after", Status: "draft", NewDamage: &noDamage, CabinClean: &clean, ParkingAllowed: &safe, KeysReturned: &safe, CarLocked: &safe, FuelLevel: &fuel, OdometerKM: &odometer, OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, PhotosConfirmedAt: &now, Version: 10}, Version: 12},
		Conversation: &dataapi.Conversation{Flow: "issue_after", Step: "collect_photos", Version: 2, Context: dataapi.ConversationContext{TargetID: &inspectionID, TripID: &tripID, ReturnID: &returnID, VehicleID: &vehicleID, IssueCategory: &category, DraftText: &description}},
	}
	rows := menuRows(dataapi.Employee{ID: employeeID, Role: "admin"}, state)
	if len(rows) > 10 {
		t.Fatalf("MAX supports at most 10 rows, got %d", len(rows))
	}
	foundAdmin := false
	for _, row := range rows {
		for _, button := range row {
			if button.Payload == "trip-list:admin:1" {
				foundAdmin = true
			}
		}
	}
	if !foundAdmin {
		t.Fatal("admin trips vanished from compact return menu")
	}
}
