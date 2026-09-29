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

func TestTripIssueDraftOwnerVersionAndDurableSave(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(ctx, driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "trip-issue-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(ctx, driver, checkout.ID, state.Checkout.Version, "trip-issue-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	if _, err := actor.CheckoutStart(ctx, driver, checkout.ID, state.Checkout.Version, "trip-issue-setup-start", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" {
		t.Fatalf("active trip: %+v %v", state, err)
	}
	trip := *state.Trip
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-card", "trip:"+trip.ID, now)); err != nil {
		t.Fatal(err)
	}
	button := fmt.Sprintf("trip-issue:%s:%d", trip.ID, trip.Version)
	found := false
	for _, row := range sender.Messages()[0].Buttons {
		if row[0].Payload == button {
			found = true
		}
	}
	if !found {
		t.Fatalf("trip card has no issue entry: %+v", sender.Messages()[0])
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "trip-issue-foreign", button, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "недоступна") {
		t.Fatalf("foreign issue entry: %v %+v", err, sender.Messages())
	}
	stale := fmt.Sprintf("trip-issue:%s:%d", trip.ID, trip.Version+1)
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-stale", stale, now)); err != nil || !strings.Contains(sender.Messages()[2].Text, "изменилась") {
		t.Fatalf("stale issue entry: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-categories", button, now)); err != nil || len(sender.Messages()[3].Buttons) != 5 {
		t.Fatalf("categories: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-issue-kind", sender.Messages()[3].Buttons[1][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[4].Text, "/issue механика") {
		t.Fatalf("category hint: %v %+v", err, sender.Messages())
	}
	const command = "/issue механика Двигатель шумит при движении"
	if err := processor.Handle(ctx, odometerTestItem(driver, "trip-issue-no-lease", command, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("draft without lease: %v", err)
	}
	event := odometerTestItem(driver, "trip-issue-save", command, now).Event
	if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	worker := inboxworker.Worker{ID: "trip-issue-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "ещё не отправлено") {
		t.Fatalf("draft save: %+v %v %+v", result, err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || state.Conversation.Flow != "issue_during" || state.ConversationVersion != 2 || state.Conversation.Context.TripID == nil || *state.Conversation.Context.TripID != trip.ID || state.Trip == nil || state.Trip.Status != "active" {
		t.Fatalf("saved draft: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, odometerTestItem(driver, "trip-issue-repeat", command, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("repeat: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.ConversationVersion != 2 {
		t.Fatalf("repeat changed draft: %+v %v", state, err)
	}
}

func TestTripIssuePhotosStaySeparateAndRetry(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	ctx := context.Background()
	checkout := readyIssueCheckout(t, actor, driver)
	noDamage := false
	if _, err := actor.InspectionUpdate(ctx, driver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "trip-photo-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatal(err)
	}
	if _, err := actor.CheckoutSetNoNewIssues(ctx, driver, checkout.ID, state.Checkout.Version, "trip-photo-setup-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatal(err)
	}
	if _, err := actor.CheckoutStart(ctx, driver, checkout.ID, state.Checkout.Version, "trip-photo-setup-start", nil); err != nil {
		t.Fatal(err)
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Trip == nil {
		t.Fatal(err)
	}
	trip := *state.Trip
	me, err := actor.Me(ctx, driver)
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	vehicle, err := actor.Vehicle(ctx, driver, trip.VehicleID)
	if err != nil {
		t.Fatal(err)
	}
	category, description, kind := "mechanical", "Шум двигателя", "photo"
	input := dataapi.ConversationSaveInput{Flow: "issue_during", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &trip.ID, TripID: &trip.ID, VehicleID: &trip.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description}}
	if _, err := actor.ConversationSave(ctx, driver, me.Employee.ID, 1, input, "trip-photo-setup-draft", nil); err != nil {
		t.Fatal(err)
	}
	fetcher := &syntheticPhotoFetcher{image: samplePhoto(t, 50)}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, PhotoStore: actor, Photos: fetcher, MAX: sender}
	if err := processor.Handle(ctx, menuItem(driver, "trip-photo-menu", now)); err != nil {
		t.Fatal(err)
	}
	var help string
	for _, row := range sender.Messages()[0].Buttons {
		if strings.HasPrefix(row[0].Payload, "trip-issue-photos:") {
			help = row[0].Payload
		}
	}
	if help == "" {
		t.Fatal("trip draft photo entry absent")
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-photo-help", help, now)); err != nil || !strings.Contains(sender.Messages()[1].Text, "0/3") {
		t.Fatalf("photo help: %v %+v", err, sender.Messages())
	}
	worker := inboxworker.Worker{ID: "trip-photo-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	deliver := func(key string) string {
		t.Helper()
		event := photoItem(driver, key, now).Event
		if _, err := store.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(ctx, 1); err != nil || result.Acked != 1 {
			t.Fatalf("photo %s: %+v %v", key, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1].Text
	}
	fetcher.err = maxsdk.ErrPhotoUnavailable
	if got := deliver("trip-photo-download-failed"); !strings.Contains(got, "Не удалось") {
		t.Fatalf("download error: %q", got)
	}
	fetcher.err = nil
	for number := 1; number <= 3; number++ {
		fetcher.image = samplePhoto(t, uint8(50+number))
		if got := deliver(fmt.Sprintf("trip-photo-%d", number)); !strings.Contains(got, fmt.Sprintf("%d/3", number)) {
			t.Fatalf("photo %d: %q", number, got)
		}
	}
	if got := deliver("trip-photo-fourth"); !strings.Contains(got, "Четвёртое не добавлено") {
		t.Fatalf("fourth photo: %q", got)
	}
	if err := processor.Handle(ctx, photoItem(driver, "trip-photo-3", now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "уже сохранено") {
		t.Fatalf("repeated photo: %v %+v", err, sender.Messages())
	}
	state, err = actor.State(ctx, driver)
	if err != nil || state.Conversation == nil || len(state.Conversation.Context.AssetIDs) != 3 || state.ConversationVersion != 5 || state.Trip == nil || len(state.Trip.BeforeInspection.OccupiedSlots) != 8 {
		t.Fatalf("staged photos changed inspection: %+v %v", state, err)
	}
	if err := processor.Handle(ctx, callbackItem(driver, "trip-photo-old-help", help, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменился") {
		t.Fatalf("stale help: %v %+v", err, sender.Messages())
	}
}
