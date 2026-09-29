package dialog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestParseAdminCloseDetailsPreservesUnknownAndRejectsBadValues(t *testing.T) {
	reason, data, message := parseAdminCloseDetails("Не удалось связаться | fuel=unknown | odo=42150 | location=55.75, 37.61 | landmark=У входа | keys=no | locked=yes")
	if message != "" || reason != "Не удалось связаться" || data == nil || data.FuelLevel != nil || data.OdometerKM == nil || *data.OdometerKM != 42150 ||
		data.Latitude == nil || *data.Latitude != 55.75 || data.Longitude == nil || *data.Longitude != 37.61 || data.Landmark == nil || *data.Landmark != "У входа" ||
		data.KeysReturned == nil || *data.KeysReturned || data.CarLocked == nil || !*data.CarLocked {
		t.Fatalf("explicit/unknown close details parsed incorrectly: reason=%q data=%+v error=%q", reason, data, message)
	}
	for _, value := range []string{
		"причина\nвторая строка | fuel=50",
		"причина | fuel=50 | fuel=75",
		"причина | location=55.75",
		"причина | landmark=У входа",
		"причина | locked=false",
		"причина | can_start_trip=yes",
		"причина | odo=421500000",
		"причина | fuel=30",
	} {
		if _, _, message := parseAdminCloseDetails(value); message == "" {
			t.Errorf("invalid admin close data accepted: %q", value)
		}
	}
}

func TestAdminCloseMAXFlowPersistsChallengeAndRecoversCommittedClose(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 19, 10, 0, 0, time.UTC)
	snapshot := filepath.Join(t.TempDir(), "admin-close.json")
	client, store, closeServer := adminIssueTestClients(t, snapshot, &now)
	defer func() { closeServer() }()

	trip := createAdminCloseTrip(t, ctx, client)
	oldVersion := trip.Version
	adminID := adminIssueAdmin
	sender := &failOnceAdminIssueTransport{RecordingTransport: &maxsdk.RecordingTransport{}}
	card := callbackItem(adminID, "admin-close-card", "trip:"+trip.ID, now)
	result := runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, card)
	startPayload := adminCloseActionPayload("start", trip.ID, trip.Version)
	if result.Acked != 1 || !hasButton(sender.Messages()[0].Buttons, startPayload) {
		t.Fatalf("active trip detail omitted admin close: %+v result=%+v", sender.Messages()[0], result)
	}

	// An old active-trip button must be rejected after the driver starts returning.
	if _, err := client.TripBeginReturn(ctx, adminIssueDriver, trip.ID, trip.Version, "admin-close-begin-return", nil); err != nil {
		t.Fatal(err)
	}
	result = runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, callbackItem(adminID, "admin-close-stale-start", startPayload, now))
	if result.Acked != 1 || !strings.Contains(sender.Messages()[1].Text, "изменилась") {
		t.Fatalf("stale admin-close version was not rejected: %+v result=%+v", sender.Messages()[1], result)
	}
	trip, err := client.Trip(ctx, adminID, trip.ID)
	if err != nil || trip.Status != "returning" || trip.Version != oldVersion+1 || trip.ReturnID == nil {
		t.Fatalf("trip did not remain safely in return flow: %+v %v", trip, err)
	}
	denied := runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, callbackItem(adminIssueDriver, "admin-close-driver-denied", adminCloseActionPayload("start", trip.ID, trip.Version), now))
	if denied.Acked != 1 || !strings.Contains(sender.Messages()[2].Text, "только администратору") {
		t.Fatalf("employee could invoke admin close: %+v", sender.Messages()[2])
	}

	// Start the durable flow from the current returning-trip version.
	if result = runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, callbackItem(adminID, "admin-close-start", adminCloseActionPayload("start", trip.ID, trip.Version), now)); result.Acked != 1 {
		t.Fatalf("could not start admin close: %+v", result)
	}
	state, err := client.State(ctx, adminID)
	if err != nil || state.Conversation == nil || state.Conversation.Flow != adminCloseFlow || state.Conversation.Step != "await_details" || state.Conversation.Context.TripVersion == nil || *state.Conversation.Context.TripVersion != trip.Version {
		t.Fatalf("trip selection was not durable: %+v %v", state, err)
	}

	input := "/adminclose Водитель недоступен | fuel=50 | odo=12010 | location=55.75,37.61 | landmark=У въезда | keys=yes | locked=no"
	inputItem := menuItem(adminID, "admin-close-details", now)
	inputItem.Event.Payload.Text = &input
	if result = runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, inputItem); result.Acked != 1 {
		t.Fatalf("could not save admin-close details: %+v", result)
	}
	confirmation := sender.Messages()[len(sender.Messages())-1]
	state, err = client.State(ctx, adminID)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "confirm" || state.Conversation.Context.AdminCloseData == nil || state.Conversation.Context.AdminCloseData.CarLocked == nil || *state.Conversation.Context.AdminCloseData.CarLocked ||
		state.Conversation.Context.AdminCloseData.KeysReturned == nil || !*state.Conversation.Context.AdminCloseData.KeysReturned || !strings.Contains(confirmation.Text, "фото после") || !strings.Contains(confirmation.Text, "машина не закрыта") {
		t.Fatalf("confirmation did not show explicit values and missing data: %+v state=%+v", confirmation, state)
	}
	if strings.Contains(confirmation.Text, "false км") || strings.Contains(confirmation.Text, "фото: 8/8") {
		t.Fatalf("confirmation invented missing values: %+v", confirmation)
	}

	menu := runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, menuItem(adminID, "admin-close-menu-resume", now))
	resumePayload := adminCloseActionPayload("resume", trip.ID, trip.Version)
	menuMessage := sender.Messages()[len(sender.Messages())-1]
	if menu.Acked != 1 || !hasButton(menuMessage.Buttons, resumePayload) {
		t.Fatalf("menu did not expose the durable admin-close draft: %+v result=%+v", menuMessage, menu)
	}

	// Restart the mock before creating the challenge; its durable trip/conversation state is reloaded.
	closeServer()
	client, store, closeServer = adminIssueTestClients(t, snapshot, &now)
	challengeEvent := callbackItem(adminID, "admin-close-challenge", adminCloseActionPayload("confirm", trip.ID, trip.Version), now)
	if result = runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, challengeEvent); result.Acked != 1 {
		t.Fatalf("could not create admin-close challenge: %+v", result)
	}
	state, err = client.State(ctx, adminID)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "challenge" || state.Conversation.Context.ChallengeID == nil || state.Conversation.Context.ChallengeVersion == nil || state.Conversation.Context.ChallengeQuestion == nil || len(state.Conversation.Context.ChallengeOptions) != 4 || state.Conversation.Context.AdminCloseData == nil {
		t.Fatalf("challenge intent was not durably saved: %+v %v", state, err)
	}
	challengeID, challengeVersion := *state.Conversation.Context.ChallengeID, *state.Conversation.Context.ChallengeVersion
	var a, b int
	if _, err := fmt.Sscanf(*state.Conversation.Context.ChallengeQuestion, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	correct, wrong := -1, 0
	for index, value := range state.Conversation.Context.ChallengeOptions {
		if value == a+b {
			correct = index
		} else {
			wrong = index
		}
	}
	if correct < 0 || wrong == correct {
		t.Fatalf("challenge options have no answer: %+v", state.Conversation.Context.ChallengeOptions)
	}

	// Restart again with the challenge persisted, then verify wrong-answer versioning.
	closeServer()
	client, store, closeServer = adminIssueTestClients(t, snapshot, &now)
	wrongPayload := fmt.Sprintf("admin-close:answer:%s:%d:%s:%d:%d", trip.ID, trip.Version, challengeID, challengeVersion, wrong)
	if result = runAdminCloseEvent(t, ctx, client, store, sender, nil, &now, callbackItem(adminID, "admin-close-wrong-answer", wrongPayload, now)); result.Acked != 1 {
		t.Fatalf("wrong answer was not safely handled: %+v", result)
	}
	state, err = client.State(ctx, adminID)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "challenge" || state.Conversation.Context.ChallengeVersion == nil || *state.Conversation.Context.ChallengeVersion != challengeVersion+1 || state.Conversation.Context.AdminCloseData == nil || state.Conversation.Context.AdminCloseData.CarLocked == nil || *state.Conversation.Context.AdminCloseData.CarLocked {
		t.Fatalf("wrong answer did not persist the new challenge and original intent: %+v %v", state, err)
	}
	challengeVersion = *state.Conversation.Context.ChallengeVersion

	commander := &adminCloseFailDoneCommander{Client: client, failDoneSave: true}
	worker := inboxworker.Worker{ID: "admin-close-recovery-test", Store: store, Processor: Bootstrap{Data: client, Commands: commander, MAX: sender}, Now: func() time.Time { return now }}
	correctPayload := fmt.Sprintf("admin-close:answer:%s:%d:%s:%d:%d", trip.ID, trip.Version, challengeID, challengeVersion, correct)
	closeEvent := callbackItem(adminID, "admin-close-correct-answer", correctPayload, now)
	if _, err := store.StoreInbox(ctx, closeEvent.Event, maxsdk.InboxIdempotencyKey(closeEvent.Event)); err != nil {
		t.Fatal(err)
	}
	first := runAdminCloseWorker(t, ctx, worker)
	if first.Retried != 1 || first.Acked != 0 || commander.closeCalls != 1 || commander.doneSaveCalls != 1 {
		t.Fatalf("injected done-save interruption was not reached after domain commit: %+v close=%d saves=%d", first, commander.closeCalls, commander.doneSaveCalls)
	}
	closed, err := client.Trip(ctx, adminID, trip.ID)
	vehicle, vehicleErr := client.Vehicle(ctx, adminID, trip.VehicleID)
	state, stateErr := client.State(ctx, adminID)
	if err != nil || vehicleErr != nil || stateErr != nil || closed.Status != "closed_by_admin" || closed.Version != trip.Version+1 || vehicle.Status != "unavailable" || !vehicle.NeedsReview || state.Conversation == nil || state.Conversation.Step != "challenge" || !hasMissingField(closed.MissingData, "after_photos") {
		t.Fatalf("admin close did not commit the intended safe state before recovery: trip=%+v vehicle=%+v conversation=%+v err=%v/%v/%v", closed, vehicle, state.Conversation, err, vehicleErr, stateErr)
	}

	// Recovery finds the same command result, saves done, and survives a lost MAX reply without closing twice.
	now = now.Add(time.Minute)
	sender.failNextButtons = true
	second := runAdminCloseWorker(t, ctx, worker)
	state, stateErr = client.State(ctx, adminID)
	if second.Retried != 1 || second.Acked != 0 || commander.closeCalls != 1 || commander.doneSaveCalls != 2 || stateErr != nil || state.Conversation == nil || state.Conversation.Step != "done" {
		t.Fatalf("recovery did not save done before simulated MAX loss: %+v close=%d saves=%d state=%+v err=%v", second, commander.closeCalls, commander.doneSaveCalls, state, stateErr)
	}
	now = now.Add(time.Minute)
	third := runAdminCloseWorker(t, ctx, worker)
	if third.Acked != 1 || commander.closeCalls != 1 {
		t.Fatalf("lost reply replay repeated the domain command: %+v close=%d", third, commander.closeCalls)
	}
	last := sender.Messages()[len(sender.Messages())-1]
	if !strings.Contains(last.Text, "уже закрыта") {
		t.Fatalf("replayed answer did not produce safe already-closed message: %+v", last)
	}
}

func createAdminCloseTrip(t *testing.T, ctx context.Context, client *dataapi.Client) dataapi.Trip {
	t.Helper()
	checkout := readyIssueCheckout(t, client, adminIssueDriver)
	noDamage := false
	if _, err := client.InspectionUpdate(ctx, adminIssueDriver, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "admin-close-setup-damage", nil); err != nil {
		t.Fatal(err)
	}
	state, err := client.State(ctx, adminIssueDriver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout state: %+v %v", state, err)
	}
	if _, err := client.CheckoutSetNoNewIssues(ctx, adminIssueDriver, checkout.ID, state.Checkout.Version, "admin-close-setup-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = client.State(ctx, adminIssueDriver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout before start: %+v %v", state, err)
	}
	if _, err := client.CheckoutStart(ctx, adminIssueDriver, state.Checkout.ID, state.Checkout.Version, "admin-close-setup-trip", nil); err != nil {
		t.Fatal(err)
	}
	state, err = client.State(ctx, adminIssueDriver)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" {
		t.Fatalf("active trip setup: %+v %v", state, err)
	}
	return *state.Trip
}

func runAdminCloseEvent(t *testing.T, ctx context.Context, client *dataapi.Client, store *dataapi.WorkerClient, sender maxsdk.Transport, commands CheckoutCommander, now *time.Time, item dataapi.InboxClaimItem) inboxworker.Result {
	t.Helper()
	if commands == nil {
		commands = client
	}
	if _, err := store.StoreInbox(ctx, item.Event, maxsdk.InboxIdempotencyKey(item.Event)); err != nil {
		t.Fatalf("store admin-close event: %v", err)
	}
	worker := inboxworker.Worker{ID: "admin-close-dialog-test", Store: store, Processor: Bootstrap{Data: client, Commands: commands, MAX: sender}, Now: func() time.Time { return *now }}
	return runAdminCloseWorker(t, ctx, worker)
}

func runAdminCloseWorker(t *testing.T, ctx context.Context, worker inboxworker.Worker) inboxworker.Result {
	t.Helper()
	result, err := worker.RunOnce(ctx, 1)
	if err != nil {
		t.Fatalf("admin-close worker failed: %+v %v", result, err)
	}
	return result
}

func hasMissingField(fields []string, wanted string) bool {
	for _, field := range fields {
		if field == wanted {
			return true
		}
	}
	return false
}

type adminCloseFailDoneCommander struct {
	*dataapi.Client
	failDoneSave  bool
	closeCalls    int
	doneSaveCalls int
}

func (c *adminCloseFailDoneCommander) TripAdminClose(ctx context.Context, actor, tripID string, version int64, reason, challengeID string, available *dataapi.AdminCloseData, key string, lease *dataapi.InboxLease) (dataapi.CommandResult, error) {
	c.closeCalls++
	return c.Client.TripAdminClose(ctx, actor, tripID, version, reason, challengeID, available, key, lease)
}

func (c *adminCloseFailDoneCommander) ConversationSave(ctx context.Context, actor, employeeID string, version int64, input dataapi.ConversationSaveInput, key string, lease *dataapi.InboxLease) (dataapi.CommandResult, error) {
	if input.Flow == adminCloseFlow && input.Step == "done" {
		c.doneSaveCalls++
		if c.failDoneSave {
			c.failDoneSave = false
			return dataapi.CommandResult{}, errors.New("synthetic done-save interruption")
		}
	}
	return c.Client.ConversationSave(ctx, actor, employeeID, version, input, key, lease)
}
