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

type noNewTripRightReader struct{ *dataapi.Client }

func (r noNewTripRightReader) Me(ctx context.Context, actor string) (dataapi.Me, error) {
	me, err := r.Client.Me(ctx, actor)
	if me.Employee != nil {
		me.Employee.CanStartTrip = false
	}
	return me, err
}

func TestBeginReturnKeepsTripOccupiedAndRecoversReply(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	current := now
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(context.Background(), driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "return-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(context.Background(), driver, checkout.ID, state.Checkout.Version, "return-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout after answer: %+v %v", state, err)
	}
	if _, err := actor.CheckoutStart(context.Background(), driver, checkout.ID, state.Checkout.Version, "return-setup-start", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" {
		t.Fatalf("active trip: %+v %v", state, err)
	}
	trip := *state.Trip
	sender := &failOnePhotoReply{}
	processor := Bootstrap{Data: noNewTripRightReader{actor}, Commands: actor, MAX: sender, Location: time.UTC}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-trip-detail", "trip:"+trip.ID, now)); err != nil {
		t.Fatal(err)
	}
	var intent string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-intent:") {
			intent = row[0].Payload
		}
	}
	if intent != fmt.Sprintf("return-intent:%s:%d", trip.ID, trip.Version) || !strings.Contains(sender.Messages()[0].Text, "Длительность:") {
		t.Fatalf("active card: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(context.Background(), callbackItem("8000000000000000002", "foreign-return-intent", intent, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "изменились") {
		t.Fatalf("foreign intent: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-intent", intent, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "готовы оформить возврат") {
		t.Fatalf("return preview: %v %+v", err, sender.Messages())
	}
	confirm := sender.Messages()[2].Buttons[0][0].Payload
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-no-lease", confirm, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("return without lease = %v", err)
	}
	stale := fmt.Sprintf("return-confirm:%s:%d", trip.ID, trip.Version+1)
	if err := processor.Handle(context.Background(), callbackItem(driver, "return-stale", stale, now)); err != nil || !strings.Contains(sender.Messages()[3].Text, "изменились") {
		t.Fatalf("stale return: %v %+v", err, sender.Messages())
	}
	event := callbackItem(driver, "return-confirm", confirm, now).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	sender.fail = true
	worker := inboxworker.Worker{ID: "return-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("lost return reply: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Trip.Status != "returning" || state.Return == nil || !returnDraftMatches(state, dataapi.Employee{ID: trip.EmployeeID}, trip.ID) || len(state.Return.Inspection.MissingSlots) != 8 || len(state.Return.Inspection.OccupiedSlots) != 0 {
		t.Fatalf("return draft after lost reply: %+v %v", state, err)
	}
	returnID := state.Return.ID
	vehicle, err := actor.Vehicle(context.Background(), driver, trip.VehicleID)
	if err != nil || vehicle.Status != "in_trip" {
		t.Fatalf("vehicle released before completion: %+v %v", vehicle, err)
	}
	current = now.Add(time.Minute)
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "занятость автомобиля сохраняются") {
		t.Fatalf("recovered return reply: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.ID != returnID {
		t.Fatalf("duplicate return created draft: %+v %v", state, err)
	}
}
