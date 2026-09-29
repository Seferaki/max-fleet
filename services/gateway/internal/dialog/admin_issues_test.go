package dialog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

const adminIssueDriver = "8000000000000000001"
const adminIssueAdmin = "8000000000000000003"

func adminIssueTestClients(t *testing.T, snapshotPath string, now *time.Time) (*dataapi.Client, *dataapi.WorkerClient, func()) {
	t.Helper()
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", snapshotPath, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	actor, err := dataapi.New(dataapi.Config{BaseURL: server.URL + "/internal/v1", Token: "synthetic-service-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "synthetic-worker-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return actor, worker, server.Close
}

func runAdminIssueEvent(t *testing.T, client *dataapi.Client, store *dataapi.WorkerClient, sender maxsdk.Transport, now *time.Time, item dataapi.InboxClaimItem) inboxworker.Result {
	t.Helper()
	ctx := context.Background()
	if _, err := store.StoreInbox(ctx, item.Event, maxsdk.InboxIdempotencyKey(item.Event)); err != nil {
		t.Fatalf("store event: %v", err)
	}
	worker := inboxworker.Worker{ID: "admin-issue-dialog-test", Store: store, Processor: Bootstrap{Data: client, Commands: client, MAX: sender}, Now: func() time.Time { return *now }}
	result, err := worker.RunOnce(ctx, 1)
	if err != nil {
		t.Fatalf("process %s: %+v %v", item.Event.EventKey, result, err)
	}
	return result
}

func createAdminIssueFixture(t *testing.T, client *dataapi.Client) (dataapi.Issue, [][]byte) {
	t.Helper()
	ctx := context.Background()
	available := true
	vehicles, err := client.Vehicles(ctx, adminIssueDriver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(vehicles.Items) == 0 {
		t.Fatalf("available vehicles: %+v %v", vehicles, err)
	}
	vehicle := vehicles.Items[0]
	created, err := client.CheckoutCreate(ctx, adminIssueDriver, vehicle.ID, vehicle.Version, "admin-issue-dialog-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	currentVehicle, err := client.Vehicle(ctx, adminIssueDriver, vehicle.ID)
	if err != nil {
		t.Fatal(err)
	}
	photos := make([][]byte, 3)
	assetIDs := make([]string, 3)
	for index := range photos {
		photos[index] = samplePhoto(t, uint8(230+index))
		key := "admin-issue-dialog-photo-" + fmt.Sprint(index+1)
		staged, err := client.StageIssueAsset(ctx, adminIssueDriver, dataapi.IssueStageInput{ScopeType: "inspection", ScopeID: hold.Inspection.ID, SourceEventKey: key, IdempotencyKey: key, ContentType: "image/png", Image: photos[index]})
		if err != nil {
			t.Fatal(err)
		}
		assetIDs[index] = staged.AssetID
	}
	inspectionID := hold.Inspection.ID
	issued, err := client.IssueCreate(ctx, adminIssueDriver, vehicle.ID, currentVehicle.Version,
		dataapi.IssueCreateInput{Category: "mechanical", Description: "Синтетическая проверка крепления", InspectionID: &inspectionID, AssetIDs: assetIDs}, "admin-issue-dialog-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](issued)
	if err != nil {
		t.Fatal(err)
	}
	return issue, photos
}

func TestAdminIssueMAXFlowRecoversCommentAndRetriesLostMAXReply(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 17, 30, 0, 0, time.UTC)
	snapshot := filepath.Join(t.TempDir(), "admin-issue.json")
	client, store, closeServer := adminIssueTestClients(t, snapshot, &now)
	issue, photos := createAdminIssueFixture(t, client)
	sender := &failOnceAdminIssueTransport{RecordingTransport: &maxsdk.RecordingTransport{}}

	denied := menuItem(adminIssueDriver, "admin-issue-list-denied", now)
	command := "/adminissues"
	denied.Event.Payload.Text = &command
	result := runAdminIssueEvent(t, client, store, sender, &now, denied)
	if result.Acked != 1 || !strings.Contains(sender.Messages()[0].Text, "только администратору") {
		t.Fatalf("non-admin issue list was not denied: %+v %+v", result, sender.Messages())
	}

	list := menuItem(adminIssueAdmin, "admin-issue-list", now)
	list.Event.Payload.Text = &command
	result = runAdminIssueEvent(t, client, store, sender, &now, list)
	if result.Acked != 1 || !strings.Contains(sender.Messages()[1].Text, "Обновлено ") || !strings.Contains(sender.Messages()[1].Text, "DEMO-001") || !strings.Contains(sender.Messages()[1].Text, "Тестовый сотрудник 1") {
		t.Fatalf("admin issue list lacks vehicle/author: %+v %+v", result, sender.Messages()[1])
	}
	if len(sender.Messages()[1].Buttons) < 3 || sender.Messages()[1].Buttons[2][0].Payload != "admin-issue:"+issue.ID {
		t.Fatalf("admin issue list action: %+v", sender.Messages()[1].Buttons)
	}

	openCard := callbackItem(adminIssueAdmin, "admin-issue-detail", "admin-issue:"+issue.ID, now)
	result = runAdminIssueEvent(t, client, store, sender, &now, openCard)
	if result.Acked != 1 || !strings.Contains(sender.Messages()[2].Text, "Синтетическая проверка крепления") {
		t.Fatalf("issue detail not opened: %+v %+v", result, sender.Messages()[2])
	}
	if len(sender.Messages()[2].Buttons) < 3 || sender.Messages()[2].Buttons[2][0].Payload != fmt.Sprintf("admin-issue-photo:%s:1:3", issue.ID) {
		t.Fatalf("issue photo actions did not expose three attached photos: %+v", sender.Messages()[2].Buttons)
	}
	photo := callbackItem(adminIssueAdmin, "admin-issue-photo", fmt.Sprintf("admin-issue-photo:%s:1:3", issue.ID), now)
	result = runAdminIssueEvent(t, client, store, sender, &now, photo)
	if result.Acked != 1 || len(sender.images) != 1 || !strings.Contains(sender.images[0], "image/png") || !strings.Contains(sender.images[0], string(photos[2])) {
		t.Fatalf("admin did not receive the selected private issue photo: %+v images=%d", result, len(sender.images))
	}

	take := callbackItem(adminIssueAdmin, "admin-issue-take", "admin-issue-take:"+issue.ID+":1", now)
	result = runAdminIssueEvent(t, client, store, sender, &now, take)
	assigned, err := client.Issue(ctx, adminIssueAdmin, issue.ID)
	admin, adminErr := client.Me(ctx, adminIssueAdmin)
	if result.Acked != 1 || err != nil || adminErr != nil || admin.Employee == nil || assigned.Status != "in_progress" || assigned.AssignedTo == nil || *assigned.AssignedTo != admin.Employee.ID || assigned.ResolutionComment != nil {
		t.Fatalf("take-work was not status-only/current-admin assigned: %+v issue=%+v me=%+v errors=%v/%v", result, assigned, admin, err, adminErr)
	}
	staleTake := callbackItem(adminIssueAdmin, "admin-issue-take-stale", "admin-issue-take:"+issue.ID+":1", now)
	result = runAdminIssueEvent(t, client, store, sender, &now, staleTake)
	unchanged, err := client.Issue(ctx, adminIssueAdmin, issue.ID)
	if result.Acked != 1 || err != nil || unchanged.Version != 2 || unchanged.Status != "in_progress" {
		t.Fatalf("stale take-work changed issue: %+v issue=%+v err=%v", result, unchanged, err)
	}

	begin := callbackItem(adminIssueAdmin, "admin-issue-begin", "admin-issue-begin:"+issue.ID+":2:resolved", now)
	result = runAdminIssueEvent(t, client, store, sender, &now, begin)
	state, err := client.State(ctx, adminIssueAdmin)
	if result.Acked != 1 || err != nil || state.Conversation == nil || state.Conversation.Flow != adminIssueResolutionFlow || state.Conversation.Step != "await_comment_resolved" {
		t.Fatalf("terminal comment step was not persisted: %+v state=%+v err=%v", result, state, err)
	}

	commentItem := menuItem(adminIssueAdmin, "admin-issue-comment", now)
	comment := "Комментарий: крепление проверено и восстановлено"
	commentItem.Event.Payload.Text = &comment
	result = runAdminIssueEvent(t, client, store, sender, &now, commentItem)
	state, err = client.State(ctx, adminIssueAdmin)
	if result.Acked != 1 || err != nil || state.Conversation == nil || state.Conversation.Step != "confirm_resolved" || state.Conversation.Context.DraftText == nil || *state.Conversation.Context.DraftText != "крепление проверено и восстановлено" {
		t.Fatalf("resolution comment not saved before confirmation: %+v state=%+v err=%v", result, state, err)
	}

	closeServer()
	client, store, closeServer = adminIssueTestClients(t, snapshot, &now)
	defer closeServer()
	state, err = client.State(ctx, adminIssueAdmin)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "confirm_resolved" || state.Conversation.Context.DraftText == nil || !strings.Contains(*state.Conversation.Context.DraftText, "восстановлено") {
		t.Fatalf("restart lost saved terminal comment: %+v %v", state, err)
	}
	resumeMenu := menuItem(adminIssueAdmin, "admin-issue-menu-resume", now)
	result = runAdminIssueEvent(t, client, store, sender, &now, resumeMenu)
	resumePayload := "admin-issue-resume:" + issue.ID + ":2"
	if result.Acked != 1 || !hasButton(sender.Messages()[len(sender.Messages())-1].Buttons, resumePayload) {
		t.Fatalf("menu did not restore pending issue decision: %+v %+v", result, sender.Messages()[len(sender.Messages())-1])
	}
	resume := callbackItem(adminIssueAdmin, "admin-issue-resume", resumePayload, now)
	runAdminIssueEvent(t, client, store, sender, &now, resume)
	if !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "крепление проверено и восстановлено") {
		t.Fatalf("resume did not show the saved comment: %+v", sender.Messages()[len(sender.Messages())-1])
	}

	confirm := callbackItem(adminIssueAdmin, "admin-issue-confirm", "admin-issue-confirm:"+issue.ID+":2:resolved", now)
	sender.failNextButtons = true
	result = runAdminIssueEvent(t, client, store, sender, &now, confirm)
	if result.Retried != 1 || result.Acked != 0 {
		t.Fatalf("simulated lost MAX reply did not keep the event pending: %+v", result)
	}
	now = now.Add(time.Minute)
	result, err = (&inboxworker.Worker{ID: "admin-issue-dialog-test", Store: store, Processor: Bootstrap{Data: client, Commands: client, MAX: sender}, Now: func() time.Time { return now }}).RunOnce(ctx, 1)
	if err != nil {
		t.Fatalf("retry terminal callback: %+v %v", result, err)
	}
	resolved, err := client.Issue(ctx, adminIssueAdmin, issue.ID)
	state, stateErr := client.State(ctx, adminIssueAdmin)
	if err != nil || stateErr != nil || result.Acked != 1 || resolved.Status != "resolved" || resolved.Version != 3 || resolved.ResolvedBy == nil || *resolved.ResolvedBy != admin.Employee.ID || resolved.ResolutionComment == nil || *resolved.ResolutionComment != "крепление проверено и восстановлено" || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.DraftText != nil {
		t.Fatalf("resolved issue did not recover exactly once: worker=%+v issue=%+v state=%+v errors=%v/%v", result, resolved, state, err, stateErr)
	}
}

func hasButton(rows [][]maxsdk.Button, payload string) bool {
	for _, row := range rows {
		for _, button := range row {
			if button.Payload == payload {
				return true
			}
		}
	}
	return false
}

type failOnceAdminIssueTransport struct {
	*maxsdk.RecordingTransport
	failNextButtons bool
	images          []string
}

func (s *failOnceAdminIssueTransport) SendButtons(ctx context.Context, userID int64, text string, rows [][]maxsdk.Button) (string, error) {
	if s.failNextButtons {
		s.failNextButtons = false
		return "", errors.New("injected MAX send loss")
	}
	return s.RecordingTransport.SendButtons(ctx, userID, text, rows)
}

func (s *failOnceAdminIssueTransport) SendImage(_ context.Context, _ int64, caption, contentType string, data []byte) (string, error) {
	s.images = append(s.images, caption+"|"+contentType+"|"+string(data))
	return "synthetic-admin-issue-photo", nil
}

type adminIssueAssetFaultReader struct {
	*dataapi.Client
	assetErr     error
	assetContent dataapi.AssetContent
	assetCalls   int
}

func (r *adminIssueAssetFaultReader) AssetContent(context.Context, string, string) (dataapi.AssetContent, error) {
	r.assetCalls++
	return r.assetContent, r.assetErr
}

func TestAdminIssuePhotoErrorsAndAuthorization(t *testing.T) {
	now := time.Date(2026, 9, 29, 17, 45, 0, 0, time.UTC)
	client, _, closeServer := adminIssueTestClients(t, filepath.Join(t.TempDir(), "admin-issue-photo.json"), &now)
	defer closeServer()
	issue, _ := createAdminIssueFixture(t, client)
	reader := &adminIssueAssetFaultReader{Client: client, assetErr: &dataapi.APIError{Status: 503, Code: "STORAGE_UNAVAILABLE", Retryable: true}}
	sender := &failOnceAdminIssueTransport{RecordingTransport: &maxsdk.RecordingTransport{}}
	processor := Bootstrap{Data: reader, MAX: sender}

	storageFailure := callbackItem(adminIssueAdmin, "admin-issue-photo-storage-failure", fmt.Sprintf("admin-issue-photo:%s:1:3", issue.ID), now)
	if err := processor.Handle(context.Background(), storageFailure); err != nil {
		t.Fatal(err)
	}
	message := sender.Messages()[0]
	if !strings.Contains(message.Text, "временно не удалось прочитать") || !hasButton(message.Buttons, fmt.Sprintf("admin-issue-photo:%s:1:3", issue.ID)) || len(sender.images) != 0 {
		t.Fatalf("storage failure did not preserve a retry for the selected photo: %+v images=%d", message, len(sender.images))
	}

	reader.assetErr = &dataapi.APIError{Status: 404, Code: "NOT_FOUND"}
	missing := callbackItem(adminIssueAdmin, "admin-issue-photo-missing", fmt.Sprintf("admin-issue-photo:%s:1:2", issue.ID), now)
	if err := processor.Handle(context.Background(), missing); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.Messages()[1].Text, "недоступно") || len(sender.images) != 0 {
		t.Fatalf("missing photo did not return a safe response: %+v", sender.Messages()[1])
	}

	reader.assetErr = nil
	reader.assetContent = dataapi.AssetContent{ContentType: "text/plain", Bytes: []byte("not an image")}
	invalid := callbackItem(adminIssueAdmin, "admin-issue-photo-invalid", fmt.Sprintf("admin-issue-photo:%s:1:1", issue.ID), now)
	if err := processor.Handle(context.Background(), invalid); err == nil || len(sender.images) != 0 {
		t.Fatalf("invalid photo content was sent: err=%v images=%d", err, len(sender.images))
	}

	reads := reader.assetCalls
	denied := callbackItem(adminIssueDriver, "admin-issue-photo-driver", fmt.Sprintf("admin-issue-photo:%s:1:1", issue.ID), now)
	if err := processor.Handle(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	if reader.assetCalls != reads || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "только администратору") || len(sender.images) != 0 {
		t.Fatalf("non-admin reached issue photo content: calls=%d->%d messages=%+v", reads, reader.assetCalls, sender.Messages())
	}
}

type adminIssueRecoveryReader struct {
	*dataapi.Client
	state  dataapi.CurrentState
	result dataapi.CommandResult
}

func (r *adminIssueRecoveryReader) State(context.Context, string) (dataapi.CurrentState, error) {
	return r.state, nil
}

func (r *adminIssueRecoveryReader) OwnCommandResult(context.Context, string, string, string) (dataapi.CommandResult, error) {
	return r.result, nil
}

type adminIssueRecoveryCommander struct {
	*dataapi.Client
	savedConversation bool
	issueResolveCalls int
}

func (c *adminIssueRecoveryCommander) IssueResolve(context.Context, string, string, int64, dataapi.IssueResolveInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error) {
	c.issueResolveCalls++
	return dataapi.CommandResult{}, errors.New("replayed command should have been recovered")
}

func (c *adminIssueRecoveryCommander) ConversationSave(_ context.Context, _ string, _ string, version int64, input dataapi.ConversationSaveInput, _ string, _ *dataapi.InboxLease) (dataapi.CommandResult, error) {
	c.savedConversation = true
	conversation := dataapi.Conversation{Flow: input.Flow, Step: input.Step, Context: input.Context, PendingInputKind: input.PendingInputKind, Version: version + 1}
	aggregate, err := json.Marshal(conversation)
	if err != nil {
		return dataapi.CommandResult{}, err
	}
	return dataapi.CommandResult{Operation: "conversation.save", Aggregate: aggregate}, nil
}

func TestAdminIssueResolutionRecoversCommittedCommandBeforeCASRead(t *testing.T) {
	now := time.Date(2026, 9, 29, 18, 0, 0, 0, time.UTC)
	client, _, closeServer := mockClients(t, now)
	defer closeServer()
	admin, err := client.Me(context.Background(), adminIssueAdmin)
	if err != nil || admin.Employee == nil {
		t.Fatal(err)
	}
	issueID := "70000000-0000-4000-8000-000000000029"
	issueVersion, comment, none := int64(1), "Проверено после повторного чтения команды", "none"
	conversation := dataapi.Conversation{Flow: adminIssueResolutionFlow, Step: "confirm_resolved", Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &issueVersion, DraftText: &comment}, PendingInputKind: &none, Version: 3}
	state := dataapi.CurrentState{Conversation: &conversation, ConversationVersion: 3}
	resolvedBy := admin.Employee.ID
	resolved := dataapi.Issue{ID: issueID, VehicleID: "10000000-0000-4000-8000-000000000001", AuthorID: "80000000-0000-4000-8000-000000000001", Status: "resolved", Category: "mechanical", Stage: "during", Description: "Синтетическая проверка", BlocksIssuance: false, ResolutionComment: &comment, ResolvedBy: &resolvedBy, Version: 2, UpdatedAt: now}
	aggregate, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	reader := &adminIssueRecoveryReader{Client: client, state: state, result: dataapi.CommandResult{Operation: "issue.resolve", Aggregate: aggregate}}
	commands := &adminIssueRecoveryCommander{Client: client}
	sender := &maxsdk.RecordingTransport{}
	item := callbackItem(adminIssueAdmin, "admin-issue-recover-confirm", "admin-issue-confirm:"+issueID+":1:resolved", now)
	item.ID, item.LeaseToken = "a7eaf5e1-4a75-4c2f-a149-7ce0b65c00d1", "synthetic-lease"
	if err := (Bootstrap{Data: reader, Commands: commands, MAX: sender}).Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if commands.issueResolveCalls != 0 || !commands.savedConversation || !strings.Contains(sender.Messages()[0].Text, "Решение сохранено") {
		t.Fatalf("committed issue command was not recovered safely: resolveCalls=%d saved=%t message=%+v", commands.issueResolveCalls, commands.savedConversation, sender.Messages())
	}
}
