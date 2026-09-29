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

func TestFullReturnThroughDialogOnMock(t *testing.T) {
	now := time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return now })
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	draft := readyChecklistDraft(t, actor, driver)
	sender := &recordedPreviousImage{RecordingTransport: &maxsdk.RecordingTransport{}}
	fetcher := &syntheticPhotoFetcher{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Photos: fetcher, PhotoStore: actor, Location: time.UTC}
	worker := inboxworker.Worker{ID: "full-return-worker", Store: store, Processor: &processor, Now: func() time.Time { return now }}
	latest := func() maxsdk.RecordedText {
		messages := sender.Messages()
		return messages[len(messages)-1]
	}
	button := func(prefix string) string {
		t.Helper()
		for _, row := range latest().Buttons {
			for _, choice := range row {
				if strings.HasPrefix(choice.Payload, prefix) {
					return choice.Payload
				}
			}
		}
		t.Fatalf("button %q missing in %+v", prefix, latest())
		return ""
	}
	deliver := func(event dataapi.NormalizedEvent) {
		t.Helper()
		if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
			t.Fatalf("event %s: %+v %v", event.EventKey, result, err)
		}
	}
	callback := func(key, payload string) {
		t.Helper()
		deliver(callbackItem(driver, key, payload, now).Event)
	}
	menu := func(key string) {
		t.Helper()
		if err := processor.Handle(ctx, menuItem(driver, key, now)); err != nil {
			t.Fatal(err)
		}
	}

	for index, field := range []string{"damage", "clean", "parking", "keys_lock"} {
		menu(fmt.Sprintf("return-flow-check-menu-%d", index))
		callback(fmt.Sprintf("return-flow-check-open-%d", index), button("return-check:"))
		answer := latest().Buttons[0][0].Payload
		if field == "damage" {
			answer = latest().Buttons[1][0].Payload
		}
		callback(fmt.Sprintf("return-flow-check-save-%d", index), answer)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Return == nil || nextReturnCheckField(state.Return.Inspection) != "" {
		t.Fatalf("checklist incomplete: %+v %v", state, err)
	}
	menu("return-flow-photo-menu")
	callback("return-flow-photo-open", button("return-photos:"))
	for slot := 1; slot <= 8; slot++ {
		fetcher.image = samplePhoto(t, uint8(150+slot))
		deliver(photoItem(driver, fmt.Sprintf("return-flow-photo-%d", slot), now).Event)
		if !strings.Contains(latest().Text, fmt.Sprintf("%d/8", slot)) {
			t.Fatalf("after photo %d: %+v", slot, latest())
		}
	}
	menu("return-flow-photo-confirm-menu")
	callback("return-flow-photo-confirm", button("return-confirm-photos:"))
	menu("return-flow-fuel-menu")
	callback("return-flow-fuel-open", button("return-fuel:"))
	callback("return-flow-fuel-save", latest().Buttons[3][0].Payload)
	menu("return-flow-odometer-menu")
	callback("return-flow-odometer-open", button("return-odometer:"))
	odometer := "/odometer 12050"
	item := menuItem(driver, "return-flow-odometer-save", now).Event
	item.Payload.Text = &odometer
	deliver(item)
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return == nil || state.Return.ParkingLocation != nil {
		t.Fatalf("unexpected location before map selection: %+v %v", state, err)
	}
	if _, err := actor.ReturnSetLocation(ctx, driver, draft.ID, state.Return.Version, "return-flow-manual-map", nil, dataapi.LocationInput{Latitude: 55.75, Longitude: 37.62, Source: "manual_map", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	menu("return-flow-summary-menu")
	callback("return-flow-summary", button("return-summary:"))
	if !strings.Contains(latest().Text, "Фото после: 8/8") || !strings.Contains(latest().Text, "manual_map") {
		t.Fatalf("return summary: %+v", latest())
	}
	callback("return-flow-complete", button("return-complete:"))
	if !strings.Contains(latest().Text, "Возврат подтверждён") {
		t.Fatalf("unconfirmed return: %+v", latest())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Return != nil || state.Trip != nil {
		t.Fatalf("completed return remains active: %+v %v", state, err)
	}
	trip, err := actor.Trip(ctx, driver, draft.TripID)
	if err != nil || trip.Status != "completed" || trip.AfterInspection == nil || len(trip.AfterInspection.OccupiedSlots) != 8 || len(trip.BeforeInspection.OccupiedSlots) != 8 || trip.ParkingLocation == nil || trip.ParkingLocation.Source != "manual_map" {
		t.Fatalf("completed trip missing 8+8 photos or parking: %+v %v", trip, err)
	}
	vehicle, err := actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("vehicle was not released after confirmed complete: %+v %v", vehicle, err)
	}
	card := menuItem(driver, "previous-flow-car", now)
	carCommand := "/car " + trip.VehicleID
	card.Event.Payload.Text = &carCommand
	if err := processor.Handle(ctx, card); err != nil {
		t.Fatal(err)
	}
	callback("previous-flow-open", button("prev:"))
	if !strings.Contains(latest().Text, "Предыдущий завершённый осмотр") {
		t.Fatalf("completed after-inspection not shown: %+v", latest())
	}
	callback("previous-flow-photo-menu", button("prev-photos:"))
	if !strings.Contains(latest().Text, "ракурс предыдущего осмотра") || len(latest().Buttons) != 9 {
		t.Fatalf("previous photo choices missing: %+v", latest())
	}
	previousPhotoPayload := button("prev-photo:" + trip.VehicleID + ":")
	callback("previous-flow-photo", previousPhotoPayload)
	expectedFirstAfterPhoto := samplePhoto(t, 151)
	if len(sender.images) != 1 || !strings.Contains(sender.images[0], "1/8") || !strings.Contains(sender.images[0], "image/png") || !strings.Contains(sender.images[0], string(expectedFirstAfterPhoto)) {
		t.Fatalf("latest after photo was not privately delivered: %+v", sender.images)
	}
	if strings.Contains(latest().Text, trip.ID) || strings.Contains(sender.images[0], trip.ID) || strings.Contains(sender.images[0], driver) {
		t.Fatalf("previous photo view exposed trip/driver identity: %+v %+v", latest(), sender.images)
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000009", "previous-flow-unknown-actor", previousPhotoPayload, now)); err != nil || len(sender.images) != 1 || !strings.Contains(latest().Text, "Доступ ещё не выдан") {
		t.Fatalf("unknown actor read previous inspection photo: %v %+v %+v", err, latest(), sender.images)
	}
	changedFuel := 25
	if _, err := actor.InspectionUpdate(ctx, driver, trip.AfterInspection.ID, trip.AfterInspection.Version, dataapi.InspectionUpdateInput{FuelLevel: &changedFuel}, "completed-trip-must-not-change-fuel", nil); err == nil {
		t.Fatal("completed after inspection accepted a fuel rewrite")
	}
	if _, err := actor.ReturnSetLocation(ctx, driver, draft.ID, draft.Version, "completed-trip-must-not-change-location", nil, dataapi.LocationInput{Latitude: 55.8, Longitude: 37.7, Source: "manual_map", Confirmed: true}); err == nil {
		t.Fatal("completed return accepted a parking rewrite")
	}
	unchanged, err := actor.Trip(ctx, driver, trip.ID)
	if err != nil || unchanged.Version != trip.Version || unchanged.AfterInspection == nil || *unchanged.AfterInspection.FuelLevel != *trip.AfterInspection.FuelLevel || unchanged.ParkingLocation == nil || unchanged.ParkingLocation.ID != trip.ParkingLocation.ID {
		t.Fatalf("completed trip snapshot changed: %+v %v", unchanged, err)
	}

	if err := processor.Handle(ctx, callbackItem(driver, "post-return-open-card", "trip:"+trip.ID, now)); err != nil {
		t.Fatal(err)
	}
	postReturnButton := button("trip-post-issue:")
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "post-return-foreign-open", postReturnButton, now)); err != nil || !strings.Contains(latest().Text, "недоступна") || len(latest().Buttons) != 0 {
		t.Fatalf("foreign actor opened post-return flow: %v %+v", err, latest())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "post-return-stale-open", fmt.Sprintf("trip-post-issue:%s:%d", trip.ID, trip.Version+1), now)); err != nil || !strings.Contains(latest().Text, "изменилась") {
		t.Fatalf("stale trip version opened post-return flow: %v %+v", err, latest())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "post-return-open", postReturnButton, now)); err != nil {
		t.Fatal(err)
	}
	categoryButton := ""
	for _, row := range latest().Buttons {
		for _, choice := range row {
			if strings.HasSuffix(choice.Payload, ":mechanical") {
				categoryButton = choice.Payload
			}
		}
	}
	if categoryButton == "" {
		t.Fatal("post-return issue categories missing")
	}
	callback("post-return-select-category", categoryButton)
	issueDescription := "/issue После поездки слышен посторонний звук"
	descriptionEvent := menuItem(driver, "post-return-save-description", now).Event
	descriptionEvent.Payload.Text = &issueDescription
	deliver(descriptionEvent)
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Flow != postReturnIssueFlow || state.Conversation.Step != "collect_photos" || state.Trip != nil {
		t.Fatalf("post-return draft was not durable without an active trip: %+v %v", state, err)
	}
	menu("post-return-draft-menu")
	reviewButton := button("trip-post-issue-review:")
	if !strings.Contains(latest().Text, "Черновик сообщения после поездки") {
		t.Fatalf("post-return draft missing from /menu: %+v", latest())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "post-return-photo-help", button("trip-post-issue-photos:"), now)); err != nil || !strings.Contains(latest().Text, "0/3") {
		t.Fatalf("post-return photo help: %v %+v", err, latest())
	}
	fetcher.err = maxsdk.ErrPhotoUnavailable
	deliver(photoItem(driver, "post-return-photo-failed", now).Event)
	if !strings.Contains(latest().Text, "Не удалось получить фото") {
		t.Fatalf("post-return photo failure was not explained: %+v", latest())
	}
	fetcher.err = nil
	fetcher.image = samplePhoto(t, 212)
	deliver(photoItem(driver, "post-return-photo-success", now).Event)
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || len(state.Conversation.Context.AssetIDs) != 1 {
		t.Fatalf("post-return photo was not persisted: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, callbackItem(driver, "post-return-stale-review", reviewButton, now)); err != nil || !strings.Contains(latest().Text, "изменился") {
		t.Fatalf("stale post-return review was accepted after photo version bump: %v %+v", err, latest())
	}
	if err := processor.Handle(ctx, photoItem(driver, "post-return-photo-success", now)); err != nil || !strings.Contains(latest().Text, "уже сохранено") {
		t.Fatalf("post-return photo replay: %v %+v", err, latest())
	}
	menu("post-return-review-menu")
	reviewButton = button("trip-post-issue-review:")
	if err := processor.Handle(ctx, callbackItem(driver, "post-return-review", reviewButton, now)); err != nil || !strings.Contains(latest().Text, "После поездки слышен посторонний звук") {
		t.Fatalf("post-return review did not show saved report: %v %+v", err, latest())
	}
	submitButton := button("trip-post-issue-submit:")
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "post-return-foreign-review", reviewButton, now)); err != nil || !strings.Contains(latest().Text, "изменился") {
		t.Fatalf("foreign post-return review leaked draft: %v %+v", err, latest())
	}
	issueEvent := callbackItem(driver, "post-return-submit", submitButton, now).Event
	if _, err := store.StoreInbox(ctx, issueEvent, maxsdk.InboxIdempotencyKey(issueEvent)); err != nil {
		t.Fatal(err)
	}
	processor.Commands = &failDoneSave{Client: actor, fail: true}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("post-return interruption after issue commit: %+v %v", result, err)
	}
	firstIssueState, err := actor.Trip(ctx, driver, trip.ID)
	if err != nil || len(firstIssueState.Issues) != 1 || firstIssueState.Issues[0].Stage != "post_return" || firstIssueState.Version != trip.Version || firstIssueState.AfterInspection == nil || firstIssueState.AfterInspection.Version != trip.AfterInspection.Version {
		t.Fatalf("post-return command changed immutable history or was not committed: %+v %v", firstIssueState, err)
	}
	processor.Commands = actor
	now = now.Add(time.Minute)
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(latest().Text, "сохранено отдельно") {
		t.Fatalf("post-return retry did not recover idempotently: %+v %v %+v", result, err, latest())
	}
	state, err = actor.State(ctx, driver)
	finalTrip, tripErr := actor.Trip(ctx, driver, trip.ID)
	postIssueVehicle, vehicleErr := actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil || tripErr != nil || vehicleErr != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.IssueID == nil || *state.Conversation.Context.IssueID != firstIssueState.Issues[0].ID || len(finalTrip.Issues) != 1 || finalTrip.Version != trip.Version || finalTrip.AfterInspection == nil || finalTrip.AfterInspection.Version != trip.AfterInspection.Version || postIssueVehicle.Status != "unavailable" || !postIssueVehicle.NeedsReview {
		t.Fatalf("post-return issue recovery or review block failed: state=%+v trip=%+v vehicle=%+v err=%v/%v/%v", state, finalTrip, postIssueVehicle, err, tripErr, vehicleErr)
	}
}
