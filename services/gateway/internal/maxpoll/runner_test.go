package maxpoll

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

type source struct {
	markerSeen int64
	updates    []model.Update
	next       int64
}

func (s *source) GetUpdates(_ context.Context, marker int64) ([]model.Update, int64, error) {
	s.markerSeen = marker
	return s.updates, s.next, nil
}

type failingStore struct {
	*dataapi.WorkerClient
	fail bool
}

type rejectTransport struct {
	maxsdk.RecordingTransport
	fail bool
}

func (s *rejectTransport) SendText(ctx context.Context, actor int64, body string) (string, error) {
	if s.fail {
		return "", errors.New("synthetic MAX send failure")
	}
	return s.RecordingTransport.SendText(ctx, actor, body)
}

func (s *failingStore) StoreInbox(ctx context.Context, event dataapi.NormalizedEvent, key string) (dataapi.InboxStored, error) {
	if s.fail {
		return dataapi.InboxStored{}, errors.New("injected store failure")
	}
	return s.WorkerClient.StoreInbox(ctx, event, key)
}

func testUpdate() model.Update {
	return model.Update{UpdateType: model.UpdateBotStarted, Timestamp: 1790586000000, ChatID: 8000000000000000001, User: &model.User{UserID: 8000000000000000001}}
}

func workerAgainst(t *testing.T, handler *datamock.Server) (*dataapi.WorkerClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler.Handler())
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "synthetic-worker-token"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return worker, server
}

func TestPollingStoresBatchBeforeDurableMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	worker, server := workerAgainst(t, mock)
	src := &source{updates: []model.Update{testUpdate(), {UpdateType: model.UpdateMessageEdited}}, next: 43}
	runner := Runner{IntegrationKey: "demo-bot", WorkerID: "poller-one", Source: src, Store: worker}
	result, err := runner.RunOnce(context.Background())
	if err != nil || result.Stored != 1 || result.Ignored != 1 || result.Marker == nil || *result.Marker != "43" || src.markerSeen != 0 {
		t.Fatalf("poll result = %+v, %v; prior marker=%d", result, err, src.markerSeen)
	}
	second := Runner{IntegrationKey: "demo-bot", WorkerID: "poller-two", Source: src, Store: worker}
	if _, err := second.RunOnce(context.Background()); err == nil {
		t.Fatal("second poller acquired active lease")
	}
	server.Close()
	restarted, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	worker, server = workerAgainst(t, restarted)
	defer server.Close()
	integration, err := worker.GetIntegration(context.Background(), "demo-bot")
	if err != nil || integration.Marker == nil || *integration.Marker != "43" {
		t.Fatalf("marker after restart = %+v, %v", integration, err)
	}
	claim, err := worker.ClaimInbox(context.Background(), "inbox-worker", 10, "claim-polling-test")
	if err != nil || len(claim.Items) != 1 || claim.Items[0].Event.Payload.Kind != "start" {
		t.Fatalf("inbox after restart = %+v, %v", claim, err)
	}
}

func TestPollingStoreFailureKeepsMarkerAndRetriesSameBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	worker, server := workerAgainst(t, mock)
	defer server.Close()
	store := &failingStore{WorkerClient: worker, fail: true}
	src := &source{updates: []model.Update{testUpdate()}, next: 43}
	runner := Runner{IntegrationKey: "demo-bot", WorkerID: "poller-one", Source: src, Store: store}
	if _, err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("store failure was acknowledged")
	}
	integration, err := worker.GetIntegration(context.Background(), "demo-bot")
	if err != nil || integration.Marker != nil {
		t.Fatalf("marker advanced before inbox save: %+v, %v", integration, err)
	}
	store.fail = false
	result, err := runner.RunOnce(context.Background())
	if err != nil || result.Stored != 1 || src.markerSeen != 0 || result.Marker == nil || *result.Marker != "43" {
		t.Fatalf("retry result = %+v, %v; prior marker=%d", result, err, src.markerSeen)
	}
}

func TestPollingInvalidPhotoDoesNotAdvanceMarker(t *testing.T) {
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", filepath.Join(t.TempDir(), "snapshot.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	worker, server := workerAgainst(t, mock)
	defer server.Close()
	update := model.Update{UpdateType: model.UpdateMessageCreated, Timestamp: 1790586000000, ChatID: 8000000000000000001, UserID: 8000000000000000001, Message: &model.MessageUpdate{Recipient: model.Recipient{ChatID: 8000000000000000001, ChatType: model.ChatTypeDialog}, Sender: model.Sender{UserID: 8000000000000000001}, Body: model.MessageBody{Mid: "multi-photo", Attachments: []model.Attachment{{Type: model.AttachImage}, {Type: model.AttachImage}}}}}
	runner := Runner{IntegrationKey: "demo-bot", WorkerID: "poller-one", Source: &source{updates: []model.Update{update}, next: 43}, Store: worker}
	if _, err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("invalid photo batch advanced")
	}
	integration, err := worker.GetIntegration(context.Background(), "demo-bot")
	if err != nil || integration.Marker != nil {
		t.Fatalf("invalid photo advanced marker: %+v, %v", integration, err)
	}
}

func TestPollingMultiPhotoNeedsSuccessfulReplyBeforeCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := datamock.NewWithSnapshotAndWorkerToken("synthetic-service-token", "synthetic-worker-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	worker, server := workerAgainst(t, mock)
	defer server.Close()
	multi := model.Update{UpdateType: model.UpdateMessageCreated, Timestamp: 1790586000000, ChatID: 8000000000000000001, UserID: 8000000000000000001, Message: &model.MessageUpdate{Recipient: model.Recipient{ChatID: 8000000000000000001, ChatType: model.ChatTypeDialog}, Sender: model.Sender{UserID: 8000000000000000001}, Body: model.MessageBody{Mid: "multi-photo", Attachments: []model.Attachment{{Type: model.AttachImage}, {Type: model.AttachImage}}}}}
	src := &source{updates: []model.Update{testUpdate(), multi}, next: 43}
	reply := &rejectTransport{fail: true}
	runner := Runner{IntegrationKey: "demo-bot", WorkerID: "poller-one", Source: src, Store: worker, Reject: reply}
	if result, err := runner.RunOnce(context.Background()); err == nil || result.Stored != 1 {
		t.Fatalf("failed reply was checkpointed: %+v, %v", result, err)
	}
	integration, err := worker.GetIntegration(context.Background(), "demo-bot")
	if err != nil || integration.Marker != nil {
		t.Fatalf("failed reply advanced marker: %+v, %v", integration, err)
	}
	reply.fail = false
	result, err := runner.RunOnce(context.Background())
	if err != nil || result.Rejected != 1 || result.Stored != 1 || result.Marker == nil || *result.Marker != "43" || src.markerSeen != 0 {
		t.Fatalf("recovered poll result = %+v, %v", result, err)
	}
	messages := reply.Messages()
	if len(messages) != 1 || messages[0].UserID != 8000000000000000001 {
		t.Fatalf("invalid media reply count/actor = %+v", messages)
	}
	claim, err := worker.ClaimInbox(context.Background(), "inbox-worker", 10, "claim-multi-photo")
	if err != nil || len(claim.Items) != 1 || claim.Items[0].Event.Payload.Kind != "start" {
		t.Fatalf("rejected photo reached inbox or valid event lost: %+v, %v", claim, err)
	}
}
