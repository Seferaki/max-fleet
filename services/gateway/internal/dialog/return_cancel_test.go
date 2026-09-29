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

func TestReturnCancelRestoresTripAndNextDraftIsFresh(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyReturnDraft(t, actor, driver)
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil {
		t.Fatalf("return trip: %+v %v", state, err)
	}
	tripID := state.Trip.ID
	created, err := actor.ChallengeCreateReturn(context.Background(), driver, draft.ID, draft.Version, tripID, state.Trip.Version-1, "cancel-setup-challenge", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](created)
	if err != nil {
		t.Fatal(err)
	}
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	for index, value := range challenge.Options {
		if value == a+b {
			if _, err := actor.ChallengeAnswer(context.Background(), driver, challenge.ID, challenge.Version, index, "cancel-setup-answer", nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Step != "checklist" {
		t.Fatalf("confirmed return: %+v %v", state, err)
	}
	photo, err := actor.UploadInspectionPhoto(context.Background(), driver, dataapi.InspectionPhotoInput{InspectionID: state.Return.Inspection.ID, Slot: 1, Version: state.Return.Inspection.Version, SourceEventKey: "cancel-old-photo", IdempotencyKey: "cancel-old-photo", ContentType: "image/png", Image: samplePhoto(t, 80)})
	if err != nil || len(photo.Inspection.OccupiedSlots) != 1 {
		t.Fatalf("staged after photo: %+v %v", photo, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil {
		t.Fatalf("return after photo: %+v %v", state, err)
	}
	if _, err := actor.ReturnSetLocation(context.Background(), driver, draft.ID, state.Return.Version, "cancel-old-location", nil, dataapi.LocationInput{Latitude: 55.75, Longitude: 37.62, Source: "manual_map", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.ParkingLocation == nil || len(state.Return.Inspection.OccupiedSlots) != 1 {
		t.Fatalf("old return data: %+v %v", state, err)
	}
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(context.Background(), menuItem(driver, "cancel-menu", now)); err != nil {
		t.Fatal(err)
	}
	var intent string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-cancel-intent:") {
			intent = row[0].Payload
		}
	}
	if intent == "" {
		t.Fatalf("cancel button missing: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "cancel-other", intent, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "изменилось") {
		t.Fatalf("foreign cancel: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "cancel-preview", intent, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "не перейдут") {
		t.Fatalf("cancel preview: %v %+v", err, sender.Messages())
	}
	confirm := sender.Messages()[2].Buttons[0][0].Payload
	if err := processor.Handle(context.Background(), callbackItem(driver, "cancel-no-lease", confirm, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("cancel without lease = %v", err)
	}
	event := callbackItem(driver, "cancel-confirm", confirm, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "cancel-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost cancel reply: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Trip.ID != tripID || state.Trip.Status != "active" || state.Return != nil {
		t.Fatalf("cancelled state: %+v %v", state, err)
	}
	vehicle, err := actor.Vehicle(context.Background(), driver, state.Trip.VehicleID)
	if err != nil || vehicle.Status != "in_trip" {
		t.Fatalf("cancel released car: %+v %v", vehicle, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Возврат отменён") {
		t.Fatalf("cancel reply recovery: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil {
		t.Fatalf("trip before fresh return: %+v %v", state, err)
	}
	createdAgain, err := actor.TripBeginReturn(context.Background(), driver, tripID, state.Trip.Version, "cancel-fresh-return", nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := dataapi.DecodeAggregate[dataapi.Return](createdAgain)
	if err != nil || fresh.ID == draft.ID || fresh.Inspection.ID == draft.Inspection.ID || fresh.ParkingLocation != nil || len(fresh.Inspection.OccupiedSlots) != 0 || len(fresh.Inspection.MissingSlots) != 8 {
		t.Fatalf("fresh return inherited cancelled data: %+v %v", fresh, err)
	}
}
