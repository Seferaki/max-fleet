package maxsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

type stubMessages struct {
	calls  int
	result model.SendMessageResult
	err    error
}

func (s *stubMessages) Send(_ context.Context, _ *maxbot.Message) (model.SendMessageResult, error) {
	s.calls++
	return s.result, s.err
}

func TestTransportRejectsInvalidSendBeforeSDK(t *testing.T) {
	sdk := &stubMessages{}
	transport := &SDKTransport{messages: sdk}
	for _, test := range []struct {
		id   int64
		text string
	}{{0, "hello"}, {-1, "hello"}, {123, " \n "}} {
		if _, err := transport.SendText(context.Background(), test.id, test.text); err == nil {
			t.Fatalf("accepted recipient %d and text %q", test.id, test.text)
		}
	}
	if sdk.calls != 0 {
		t.Fatalf("SDK called for invalid message %d times", sdk.calls)
	}
	if _, err := NewTransport(nil); err == nil {
		t.Fatal("accepted nil SDK")
	}
}

func TestSDKTransportResultAndError(t *testing.T) {
	sdk := &stubMessages{result: model.SendMessageResult{Message: model.Message{Body: model.MessageBody{Mid: "max-message-1"}}}}
	transport := &SDKTransport{messages: sdk}
	id, err := transport.SendText(context.Background(), 123, "Выберите машину")
	if err != nil || id != "max-message-1" || sdk.calls != 1 {
		t.Fatalf("send result = %q, %v; calls = %d", id, err, sdk.calls)
	}
	sdk.err = errors.New("MAX unavailable")
	if _, err := transport.SendText(context.Background(), 123, "Повтор"); !errors.Is(err, sdk.err) {
		t.Fatalf("SDK error lost: %v", err)
	}
	sdk.err = nil
	sdk.result = model.SendMessageResult{}
	if _, err := transport.SendText(context.Background(), 123, "Повтор"); err == nil {
		t.Fatal("accepted response without message ID")
	}
}

func TestSDKTransportSendsThroughPinnedSDK(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/messages" || r.URL.Query().Get("user_id") != "123" || r.Header.Get("Authorization") != "synthetic-test-token" {
			t.Errorf("wrong MAX request: method=%s path=%s user=%s auth=%t", r.Method, r.URL.Path, r.URL.Query().Get("user_id"), r.Header.Get("Authorization") == "synthetic-test-token")
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text != "Выберите машину" {
			t.Errorf("wrong MAX body: text=%q err=%v", body.Text, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"body":{"mid":"max-message-1"}}}`))
	}))
	defer server.Close()
	api, err := maxbot.NewApi("synthetic-test-token", maxbot.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewTransport(api)
	if err != nil {
		t.Fatal(err)
	}
	id, err := transport.SendText(context.Background(), 123, "Выберите машину")
	if err != nil || id != "max-message-1" || calls != 1 {
		t.Fatalf("send through SDK = %q, %v; calls=%d", id, err, calls)
	}
}

func TestRecordingTransportCapturesMessagesAndContext(t *testing.T) {
	var transport RecordingTransport
	id, err := transport.SendText(context.Background(), 123, "Меню")
	if err != nil || id != "recorded-1" {
		t.Fatalf("first send = %q, %v", id, err)
	}
	messages := transport.Messages()
	if len(messages) != 1 || messages[0] != (RecordedText{UserID: 123, Text: "Меню", ID: "recorded-1"}) {
		t.Fatalf("recorded messages = %+v", messages)
	}
	messages[0].Text = "changed"
	if transport.Messages()[0].Text != "Меню" {
		t.Fatal("recorded messages leaked mutable state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.SendText(ctx, 123, "После отмены"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context = %v", err)
	}
	if len(transport.Messages()) != 1 {
		t.Fatal("canceled send was recorded")
	}
}
