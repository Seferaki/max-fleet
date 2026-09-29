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

func TestAdminOdometerMAXFlowPersistsConfirmationAndRecoversCommittedCorrection(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 18, 30, 0, 0, time.UTC)
	snapshot := filepath.Join(t.TempDir(), "admin-odometer.json")
	client, store, closeServer := adminIssueTestClients(t, snapshot, &now)
	defer func() { closeServer() }()

	available := true
	vehicles, err := client.Vehicles(ctx, adminIssueDriver, dataapi.VehicleFilter{Available: &available, Limit: 1})
	if err != nil || len(vehicles.Items) != 1 {
		t.Fatalf("available vehicle: %+v %v", vehicles, err)
	}
	vehicle := vehicles.Items[0]
	created, err := client.CheckoutCreate(ctx, adminIssueDriver, vehicle.ID, vehicle.Version, "admin-odo-dialog-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	vehicle, err = client.Vehicle(ctx, adminIssueAdmin, vehicle.ID)
	if err != nil || vehicle.Status != "holding" || vehicle.CurrentOdometerKM == nil {
		t.Fatalf("held vehicle snapshot: %+v %v", vehicle, err)
	}
	oldOdometer := *vehicle.CurrentOdometerKM
	newOdometer := oldOdometer - 100
	reason := "Проверено по фото панели"
	sender := &failOnceAdminIssueTransport{RecordingTransport: &maxsdk.RecordingTransport{}}

	command := "/adminodo"
	denied := menuItem(adminIssueDriver, "admin-odo-driver-denied", now)
	denied.Event.Payload.Text = &command
	result := runAdminIssueEvent(t, client, store, sender, &now, denied)
	if result.Acked != 1 || !strings.Contains(sender.Messages()[0].Text, "только администратору") {
		t.Fatalf("non-admin correction was not denied: %+v %+v", result, sender.Messages())
	}

	list := menuItem(adminIssueAdmin, "admin-odo-list", now)
	list.Event.Payload.Text = &command
	result = runAdminIssueEvent(t, client, store, sender, &now, list)
	selectPayload := fmt.Sprintf("admin-odo:select:%s:%d", vehicle.ID, vehicle.Version)
	if result.Acked != 1 || !strings.Contains(sender.Messages()[1].Text, "holding") && !strings.Contains(sender.Messages()[1].Text, "удержании") || !hasButton(sender.Messages()[1].Buttons, selectPayload) {
		t.Fatalf("admin active-vehicle list is incorrect: %+v %+v", result, sender.Messages()[1])
	}

	selectItem := callbackItem(adminIssueAdmin, "admin-odo-select", selectPayload, now)
	result = runAdminIssueEvent(t, client, store, sender, &now, selectItem)
	state, err := client.State(ctx, adminIssueAdmin)
	if result.Acked != 1 || err != nil || state.Conversation == nil || state.Conversation.Flow != adminOdometerCorrectionFlow || state.Conversation.Step != "await_value" || state.Conversation.Context.VehicleID == nil || *state.Conversation.Context.VehicleID != vehicle.ID {
		t.Fatalf("correction target was not durably selected: %+v state=%+v err=%v", result, state, err)
	}

	input := fmt.Sprintf("/adminodo %d | %s", newOdometer, reason)
	inputItem := menuItem(adminIssueAdmin, "admin-odo-input", now)
	inputItem.Event.Payload.Text = &input
	result = runAdminIssueEvent(t, client, store, sender, &now, inputItem)
	state, err = client.State(ctx, adminIssueAdmin)
	preview := sender.Messages()[len(sender.Messages())-1]
	if result.Acked != 1 || err != nil || state.Conversation == nil || state.Conversation.Step != "confirm" ||
		!strings.Contains(preview.Text, fmt.Sprintf("Было: %d км", oldOdometer)) ||
		!strings.Contains(preview.Text, fmt.Sprintf("Станет: %d км", newOdometer)) || !strings.Contains(preview.Text, reason) ||
		!strings.Contains(preview.Text, "фотографии сохранятся") {
		t.Fatalf("saved correction did not show the exact old/new values and scope: %+v state=%+v err=%v", preview, state, err)
	}

	menu := menuItem(adminIssueAdmin, "admin-odo-menu-resume", now)
	result = runAdminIssueEvent(t, client, store, sender, &now, menu)
	resumePayload := fmt.Sprintf("admin-odo:resume:%s:%d", vehicle.ID, vehicle.Version)
	if result.Acked != 1 || !hasButton(sender.Messages()[len(sender.Messages())-1].Buttons, resumePayload) {
		t.Fatalf("menu did not expose saved correction: %+v %+v", result, sender.Messages()[len(sender.Messages())-1])
	}

	// The durable conversation must survive a mock process restart before the final confirmation.
	closeServer()
	client, store, closeServer = adminIssueTestClients(t, snapshot, &now)
	state, err = client.State(ctx, adminIssueAdmin)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "confirm" || state.Conversation.Context.CorrectionOdometerKM == nil || *state.Conversation.Context.CorrectionOdometerKM != newOdometer {
		t.Fatalf("restart lost saved odometer draft: %+v %v", state, err)
	}

	reader := &adminOdometerTestReader{Client: client}
	commander := &adminOdometerFailDoneCommander{Client: client, failDoneSave: true}
	worker := inboxworker.Worker{ID: "admin-odometer-dialog-test", Store: store, Processor: Bootstrap{Data: reader, Commands: commander, MAX: sender}, Now: func() time.Time { return now }}
	resume := callbackItem(adminIssueAdmin, "admin-odo-resume", resumePayload, now)
	if result = storeAndRunAdminOdometerEvent(t, ctx, worker, store, resume); result.Acked != 1 {
		t.Fatalf("could not resume after restart: %+v", result)
	}

	staleConfirm := callbackItem(adminIssueAdmin, "admin-odo-stale-confirm", fmt.Sprintf("admin-odo:confirm:%s:%d", vehicle.ID, vehicle.Version+1), now)
	if result = storeAndRunAdminOdometerEvent(t, ctx, worker, store, staleConfirm); result.Acked != 1 {
		t.Fatalf("stale confirmation was not safely acknowledged: %+v", result)
	}
	unchanged, err := client.Vehicle(ctx, adminIssueAdmin, vehicle.ID)
	if err != nil || unchanged.Version != vehicle.Version || unchanged.CurrentOdometerKM == nil || *unchanged.CurrentOdometerKM != oldOdometer {
		t.Fatalf("stale confirmation changed vehicle: %+v %v", unchanged, err)
	}

	confirm := callbackItem(adminIssueAdmin, "admin-odo-confirm", fmt.Sprintf("admin-odo:confirm:%s:%d", vehicle.ID, vehicle.Version), now)
	if result = storeAndRunAdminOdometerEvent(t, ctx, worker, store, confirm); result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("simulated lost conversation-save response did not retain confirmation for retry: %+v", result)
	}
	corrected, err := client.Vehicle(ctx, adminIssueAdmin, vehicle.ID)
	if err != nil || corrected.Version != vehicle.Version+1 || corrected.Status != "holding" || corrected.CurrentOdometerKM == nil || *corrected.CurrentOdometerKM != newOdometer {
		t.Fatalf("correction was not committed before retry: %+v %v", corrected, err)
	}
	state, err = client.State(ctx, adminIssueAdmin)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "confirm" {
		t.Fatalf("failed done-save should leave a recoverable confirmation: %+v %v", state, err)
	}

	now = now.Add(time.Minute)
	sender.failNextButtons = true
	result, err = worker.RunOnce(ctx, 1)
	if err != nil || result.Retried != 1 || result.Acked != 0 || commander.correctionCalls != 1 || reader.ownResultCalls != 2 {
		t.Fatalf("retry did not recover the committed command before losing the MAX reply: result=%+v calls=%d own_results=%d err=%v", result, commander.correctionCalls, reader.ownResultCalls, err)
	}
	state, err = client.State(ctx, adminIssueAdmin)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.VehicleVersion == nil || *state.Conversation.Context.VehicleVersion != vehicle.Version+1 {
		t.Fatalf("successful command recovery did not durably finish before the simulated reply loss: %+v %v", state, err)
	}
	now = now.Add(time.Minute)
	result, err = worker.RunOnce(ctx, 1)
	if err != nil || result.Acked != 1 || commander.correctionCalls != 1 || commander.doneSaveCalls != 2 || reader.ownResultCalls != 2 {
		t.Fatalf("retry after lost MAX reply did not report saved correction without repeating it: result=%+v correction_calls=%d done_saves=%d own_results=%d err=%v", result, commander.correctionCalls, commander.doneSaveCalls, reader.ownResultCalls, err)
	}
	state, err = client.State(ctx, adminIssueAdmin)
	driverState, driverStateErr := client.State(ctx, adminIssueDriver)
	if err != nil || driverStateErr != nil || state.Conversation == nil || state.Conversation.Step != "done" ||
		state.Conversation.Context.CorrectionOdometerKM != nil || driverState.Checkout == nil || driverState.Checkout.ID != checkout.ID ||
		driverState.Checkout.Status != "holding" || driverState.Checkout.Inspection.Version != checkout.Inspection.Version {
		t.Fatalf("correction recovery changed assignment or left draft pending: admin=%+v driver=%+v errors=%v/%v", state, driverState, err, driverStateErr)
	}
	lastMessage := sender.Messages()[len(sender.Messages())-1]
	if !strings.Contains(lastMessage.Text, "Коррекция уже сохранена") || !strings.Contains(lastMessage.Text, fmt.Sprintf("%d км", newOdometer)) || strings.Contains(lastMessage.Text, "устарело") {
		t.Fatalf("success message should show the corrected value, not a missing old value: %+v", lastMessage)
	}
}

func storeAndRunAdminOdometerEvent(t *testing.T, ctx context.Context, worker inboxworker.Worker, store *dataapi.WorkerClient, item dataapi.InboxClaimItem) inboxworker.Result {
	t.Helper()
	if _, err := store.StoreInbox(ctx, item.Event, maxsdk.InboxIdempotencyKey(item.Event)); err != nil {
		t.Fatalf("store %s: %v", item.Event.EventKey, err)
	}
	result, err := worker.RunOnce(ctx, 1)
	if err != nil {
		t.Fatalf("process %s: %+v %v", item.Event.EventKey, result, err)
	}
	return result
}

func TestAdminOdometerMAXFlowCorrectsVehicleDuringActiveTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 19, 0, 0, 0, time.UTC)
	client, store, closeServer := adminIssueTestClients(t, filepath.Join(t.TempDir(), "admin-odometer-trip.json"), &now)
	defer closeServer()

	checkout := readyIssueCheckout(t, client, adminIssueDriver)
	state, err := client.State(ctx, adminIssueDriver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("ready checkout: %+v %v", state, err)
	}
	noDamage := false
	if _, err := client.InspectionUpdate(ctx, adminIssueDriver, checkout.Inspection.ID, checkout.Inspection.Version,
		dataapi.InspectionUpdateInput{NewDamage: &noDamage}, "admin-odo-trip-damage", nil); err != nil {
		t.Fatal(err)
	}
	state, err = client.State(ctx, adminIssueDriver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout after inspection update: %+v %v", state, err)
	}
	if _, err := client.CheckoutSetNoNewIssues(ctx, adminIssueDriver, checkout.ID, state.Checkout.Version, "admin-odo-trip-no-issues", nil); err != nil {
		t.Fatal(err)
	}
	state, err = client.State(ctx, adminIssueDriver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout before start: %+v %v", state, err)
	}
	inspectionVersionBeforeStart := state.Checkout.Inspection.Version
	if _, err := client.CheckoutStart(ctx, adminIssueDriver, checkout.ID, state.Checkout.Version, "admin-odo-trip-start", nil); err != nil {
		t.Fatal(err)
	}
	state, err = client.State(ctx, adminIssueDriver)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" || state.Trip.BeforeInspection.Version != inspectionVersionBeforeStart+1 {
		var trip *dataapi.Trip
		if state.Trip != nil {
			trip = state.Trip
		}
		t.Fatalf("active trip setup: trip=%+v beforeVersionExpected=%d err=%v", trip, inspectionVersionBeforeStart+1, err)
	}
	tripBefore := *state.Trip
	vehicle, err := client.Vehicle(ctx, adminIssueAdmin, state.Trip.VehicleID)
	if err != nil || vehicle.Status != "in_trip" || vehicle.CurrentOdometerKM == nil {
		t.Fatalf("in-trip vehicle: %+v %v", vehicle, err)
	}
	oldOdometer, newOdometer := *vehicle.CurrentOdometerKM, *vehicle.CurrentOdometerKM-100
	reason := "Показание сверено при активной поездке"
	sender := &maxsdk.RecordingTransport{}

	command := "/adminodo"
	list := menuItem(adminIssueAdmin, "admin-odo-trip-list", now)
	list.Event.Payload.Text = &command
	result := runAdminIssueEvent(t, client, store, sender, &now, list)
	selectPayload := fmt.Sprintf("admin-odo:select:%s:%d", vehicle.ID, vehicle.Version)
	if result.Acked != 1 || !strings.Contains(sender.Messages()[0].Text, "в поездке") || !hasButton(sender.Messages()[0].Buttons, selectPayload) {
		t.Fatalf("active trip was not offered for correction: %+v %+v", result, sender.Messages()[0])
	}
	selectItem := callbackItem(adminIssueAdmin, "admin-odo-trip-select", selectPayload, now)
	if result = runAdminIssueEvent(t, client, store, sender, &now, selectItem); result.Acked != 1 {
		t.Fatalf("select active trip vehicle: %+v", result)
	}
	input := fmt.Sprintf("/adminodo %d | %s", newOdometer, reason)
	inputItem := menuItem(adminIssueAdmin, "admin-odo-trip-input", now)
	inputItem.Event.Payload.Text = &input
	if result = runAdminIssueEvent(t, client, store, sender, &now, inputItem); result.Acked != 1 {
		t.Fatalf("save in-trip correction: %+v", result)
	}
	preview := sender.Messages()[len(sender.Messages())-1]
	if !strings.Contains(preview.Text, fmt.Sprintf("Было: %d км", oldOdometer)) || !strings.Contains(preview.Text, fmt.Sprintf("Станет: %d км", newOdometer)) {
		t.Fatalf("in-trip preview did not show the old and new readings: %+v", preview)
	}
	confirm := callbackItem(adminIssueAdmin, "admin-odo-trip-confirm", fmt.Sprintf("admin-odo:confirm:%s:%d", vehicle.ID, vehicle.Version), now)
	if result = runAdminIssueEvent(t, client, store, sender, &now, confirm); result.Acked != 1 {
		t.Fatalf("confirm in-trip correction: %+v", result)
	}
	corrected, err := client.Vehicle(ctx, adminIssueAdmin, vehicle.ID)
	state, stateErr := client.State(ctx, adminIssueDriver)
	if err != nil || stateErr != nil || corrected.Version != vehicle.Version+1 || corrected.Status != "in_trip" || corrected.CurrentOdometerKM == nil || *corrected.CurrentOdometerKM != newOdometer ||
		state.Trip == nil || state.Trip.ID != tripBefore.ID || state.Trip.Version != tripBefore.Version || state.Trip.BeforeInspection.Version != tripBefore.BeforeInspection.Version || len(state.Trip.BeforeInspection.OccupiedSlots) != 8 {
		t.Fatalf("active assignment or immutable before-inspection changed: vehicle=%+v trip=%+v errors=%v/%v", corrected, state.Trip, err, stateErr)
	}
}

func TestAdminOdometerPermanentAPIErrorsAreSafeAndDoNotRetryInbox(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
	}{
		{name: "invalid request", status: 400, code: "INVALID_REQUEST"},
		{name: "revoked admin", status: 403, code: "ADMIN_REQUIRED"},
		{name: "missing vehicle", status: 404, code: "NOT_FOUND"},
		{name: "business validation", status: 422, code: "RULES_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 29, 19, 15, 0, 0, time.UTC)
			client, store, closeServer := adminIssueTestClients(t, filepath.Join(t.TempDir(), "admin-odometer-error.json"), &now)
			defer closeServer()
			available := true
			page, err := client.Vehicles(ctx, adminIssueDriver, dataapi.VehicleFilter{Available: &available, Limit: 1})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("available vehicle: %+v %v", page, err)
			}
			vehicle := page.Items[0]
			if _, err := client.CheckoutCreate(ctx, adminIssueDriver, vehicle.ID, vehicle.Version, "admin-odo-error-hold", nil); err != nil {
				t.Fatal(err)
			}
			vehicle, err = client.Vehicle(ctx, adminIssueAdmin, vehicle.ID)
			if err != nil || vehicle.Status != "holding" || vehicle.CurrentOdometerKM == nil {
				t.Fatalf("held vehicle: %+v %v", vehicle, err)
			}
			admin, err := client.Me(ctx, adminIssueAdmin)
			if err != nil || admin.Employee == nil {
				t.Fatalf("admin lookup: %+v %v", admin, err)
			}
			state, err := client.State(ctx, adminIssueAdmin)
			if err != nil {
				t.Fatal(err)
			}
			version, pending, none := vehicle.Version, "text", "none"
			awaitValue := dataapi.ConversationSaveInput{Flow: adminOdometerCorrectionFlow, Step: "await_value", PendingInputKind: &pending,
				Context: dataapi.ConversationContext{VehicleID: &vehicle.ID, VehicleVersion: &version}}
			if _, err := client.ConversationSave(ctx, adminIssueAdmin, admin.Employee.ID, state.ConversationVersion, awaitValue, "admin-odo-error-await", nil); err != nil {
				t.Fatalf("save await-value state: %v", err)
			}
			newValue, reason := *vehicle.CurrentOdometerKM-50, "Сверили фото панели"
			confirmation := dataapi.ConversationSaveInput{Flow: adminOdometerCorrectionFlow, Step: "confirm", PendingInputKind: &none,
				Context: dataapi.ConversationContext{VehicleID: &vehicle.ID, VehicleVersion: &version, CorrectionOdometerKM: &newValue, DraftText: &reason}}
			state, err = client.State(ctx, adminIssueAdmin)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ConversationSave(ctx, adminIssueAdmin, admin.Employee.ID, state.ConversationVersion, confirmation, "admin-odo-error-confirm", nil); err != nil {
				t.Fatalf("save confirm state: %v", err)
			}

			commander := &adminOdometerAPIErrorCommander{Client: client, failure: &dataapi.APIError{Status: tc.status, Code: tc.code, Message: "do not show this raw backend detail"}}
			sender := &maxsdk.RecordingTransport{}
			worker := inboxworker.Worker{ID: "admin-odometer-error-test", Store: store, Processor: Bootstrap{Data: client, Commands: commander, MAX: sender}, Now: func() time.Time { return now }}
			item := callbackItem(adminIssueAdmin, "admin-odo-error-"+tc.name, fmt.Sprintf("admin-odo:confirm:%s:%d", vehicle.ID, vehicle.Version), now)
			result := storeAndRunAdminOdometerEvent(t, ctx, worker, store, item)
			updated, readErr := client.Vehicle(ctx, adminIssueAdmin, vehicle.ID)
			state, stateErr := client.State(ctx, adminIssueAdmin)
			message := sender.Messages()[len(sender.Messages())-1]
			if result.Acked != 1 || result.Retried != 0 || commander.calls != 1 || readErr != nil || updated.Version != vehicle.Version || updated.CurrentOdometerKM == nil || *updated.CurrentOdometerKM != *vehicle.CurrentOdometerKM ||
				stateErr != nil || state.Conversation == nil || state.Conversation.Step != "confirm" || strings.Contains(message.Text, "do not show this raw backend detail") {
				t.Fatalf("permanent API error was retried, leaked, or changed state: result=%+v calls=%d message=%+v vehicle=%+v conversation=%+v errors=%v/%v", result, commander.calls, message, updated, state.Conversation, readErr, stateErr)
			}
			if !strings.Contains(message.Text, "Коррекция не") && !strings.Contains(message.Text, "исправление. Машина не") {
				t.Fatalf("permanent API error did not get a safe action message: %+v", message)
			}
		})
	}
}

type adminOdometerTestReader struct {
	*dataapi.Client
	ownResultCalls int
}

func (r *adminOdometerTestReader) OwnCommandResult(ctx context.Context, actor, key, operation string) (dataapi.CommandResult, error) {
	r.ownResultCalls++
	return r.Client.OwnCommandResult(ctx, actor, key, operation)
}

type adminOdometerFailDoneCommander struct {
	*dataapi.Client
	failDoneSave    bool
	correctionCalls int
	doneSaveCalls   int
}

type adminOdometerAPIErrorCommander struct {
	*dataapi.Client
	failure *dataapi.APIError
	calls   int
}

func (c *adminOdometerAPIErrorCommander) VehicleCorrectSnapshot(context.Context, string, string, int64, dataapi.VehicleSnapshotCorrectionInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error) {
	c.calls++
	return dataapi.CommandResult{}, c.failure
}

func (c *adminOdometerFailDoneCommander) VehicleCorrectSnapshot(ctx context.Context, actor, vehicleID string, version int64, input dataapi.VehicleSnapshotCorrectionInput, key string, lease *dataapi.InboxLease) (dataapi.CommandResult, error) {
	c.correctionCalls++
	return c.Client.VehicleCorrectSnapshot(ctx, actor, vehicleID, version, input, key, lease)
}

func (c *adminOdometerFailDoneCommander) ConversationSave(ctx context.Context, actor, employeeID string, version int64, input dataapi.ConversationSaveInput, key string, lease *dataapi.InboxLease) (dataapi.CommandResult, error) {
	if input.Flow == adminOdometerCorrectionFlow && input.Step == "done" {
		c.doneSaveCalls++
		if c.failDoneSave {
			c.failDoneSave = false
			return dataapi.CommandResult{}, errors.New("synthetic conversation-save interruption")
		}
	}
	return c.Client.ConversationSave(ctx, actor, employeeID, version, input, key, lease)
}
