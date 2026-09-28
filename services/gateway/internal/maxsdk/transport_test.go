package maxsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

type stubMessages struct {
	calls        int
	result       model.SendMessageResult
	err          error
	answerCalls  int
	answerResult model.SimpleQueryResult
	answerErr    error
}

func (s *stubMessages) Send(_ context.Context, _ *maxbot.Message) (model.SendMessageResult, error) {
	s.calls++
	return s.result, s.err
}

func (s *stubMessages) AnswerOnCallback(_ context.Context, _ string, _ model.CallbackAnswer) (model.SimpleQueryResult, error) {
	s.answerCalls++
	return s.answerResult, s.answerErr
}

func TestSDKTransportAnswersCallbackThroughPinnedSDK(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/answers" || r.URL.Query().Get("callback_id") != "synthetic-click-1" {
			t.Errorf("wrong MAX callback request: %s %s", r.Method, r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 0 {
			t.Errorf("unexpected callback answer body: size=%d err=%v", len(body), err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
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
	if err := transport.AnswerCallback(context.Background(), "synthetic-click-1"); err != nil || calls != 1 {
		t.Fatalf("SDK callback answer = %v, calls=%d", err, calls)
	}
}

func TestSDKTransportCallbackResultAndValidation(t *testing.T) {
	sdk := &stubMessages{answerResult: model.SimpleQueryResult{Success: true}}
	transport := &SDKTransport{messages: sdk}
	for _, id := range []string{"", strings.Repeat("x", 156), "\xff"} {
		if err := transport.AnswerCallback(context.Background(), id); err == nil {
			t.Fatalf("accepted callback ID %q", id)
		}
	}
	if sdk.answerCalls != 0 {
		t.Fatal("SDK called for invalid callback ID")
	}
	if err := transport.AnswerCallback(context.Background(), "click-1"); err != nil {
		t.Fatal(err)
	}
	sdk.answerResult.Success = false
	if err := transport.AnswerCallback(context.Background(), "click-2"); err == nil {
		t.Fatal("accepted unsuccessful MAX result")
	}
	sdk.answerErr = errors.New("MAX unavailable")
	if err := transport.AnswerCallback(context.Background(), "click-3"); !errors.Is(err, sdk.answerErr) {
		t.Fatalf("SDK error lost: %v", err)
	}
}

func TestTransportRejectsInvalidSendBeforeSDK(t *testing.T) {
	sdk := &stubMessages{}
	transport := &SDKTransport{messages: sdk}
	for _, test := range []struct {
		id   int64
		text string
	}{{0, "hello"}, {-1, "hello"}, {123, " \n "}, {123, strings.Repeat("а", 4001)}} {
		if _, err := transport.SendText(context.Background(), test.id, test.text); err == nil {
			t.Fatalf("accepted recipient %d and text %q", test.id, test.text)
		}
	}
	if sdk.calls != 0 {
		t.Fatalf("SDK called for invalid message %d times", sdk.calls)
	}
	for _, rows := range [][][]Button{nil, {{}}, {{{Text: "", Payload: "cars:1"}}}, {{{Text: "Далее", Payload: strings.Repeat("x", 201)}}}, {{{Text: "Карта", URL: "http://example.com"}}}, {{{Text: "Карта", URL: "https://example.com", Payload: "cars:1"}}}} {
		if _, err := transport.SendButtons(context.Background(), 123, "Список", rows); err == nil {
			t.Fatalf("invalid buttons accepted: %+v", rows)
		}
	}
	if sdk.calls != 0 {
		t.Fatalf("SDK called for invalid keyboard %d times", sdk.calls)
	}
	if _, err := NewTransport(nil); err == nil {
		t.Fatal("accepted nil SDK")
	}
}

func TestSDKTransportSendsPinnedLinkButton(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Attachments []struct {
				Payload struct {
					Buttons [][]struct{ Type, URL string } `json:"buttons"`
				} `json:"payload"`
			} `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Attachments) != 1 || len(body.Attachments[0].Payload.Buttons) != 1 || body.Attachments[0].Payload.Buttons[0][0].Type != "link" || body.Attachments[0].Payload.Buttons[0][0].URL != "https://www.openstreetmap.org/" {
			t.Errorf("wrong SDK link envelope: %+v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"body":{"mid":"max-link-1"}}}`))
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
	if id, err := transport.SendButtons(context.Background(), 123, "Карта", [][]Button{{{Text: "Показать", URL: "https://www.openstreetmap.org/"}}}); err != nil || id != "max-link-1" {
		t.Fatalf("SDK link send = %q %v", id, err)
	}
}

func TestSDKTransportSendsPinnedInlineKeyboard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text        string `json:"text"`
			Attachments []struct {
				Type    string `json:"type"`
				Payload struct {
					Buttons [][]struct {
						Text    string `json:"text"`
						Type    string `json:"type"`
						Payload string `json:"payload"`
					} `json:"buttons"`
				} `json:"payload"`
			} `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text != "Список" || len(body.Attachments) != 1 || body.Attachments[0].Type != "inline_keyboard" {
			t.Errorf("wrong SDK keyboard envelope: err=%v type=%+v", err, body.Attachments)
		} else {
			buttons := body.Attachments[0].Payload.Buttons
			if len(buttons) != 1 || len(buttons[0]) != 1 || buttons[0][0].Text != "Далее" || buttons[0][0].Type != "callback" || buttons[0][0].Payload != "cars:2" {
				t.Errorf("wrong SDK callback buttons: %+v", buttons)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"body":{"mid":"max-buttons-1"}}}`))
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
	id, err := transport.SendButtons(context.Background(), 123, "Список", [][]Button{{{Text: "Далее", Payload: "cars:2"}}})
	if err != nil || id != "max-buttons-1" {
		t.Fatalf("SDK button send = %q, %v", id, err)
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
	if len(messages) != 1 || messages[0].UserID != 123 || messages[0].Text != "Меню" || messages[0].ID != "recorded-1" || messages[0].Buttons != nil {
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

func TestRecordingTransportCopiesCallbackButtons(t *testing.T) {
	var transport RecordingTransport
	rows := [][]Button{{{Text: "Далее", Payload: "cars:2"}}}
	id, err := transport.SendButtons(context.Background(), 123, "Список", rows)
	if err != nil || id != "recorded-1" {
		t.Fatalf("recorded button send = %q, %v", id, err)
	}
	rows[0][0].Payload = "changed"
	messages := transport.Messages()
	if messages[0].Buttons[0][0].Payload != "cars:2" {
		t.Fatal("caller changed recorded callback")
	}
	messages[0].Buttons[0][0].Payload = "changed-again"
	if transport.Messages()[0].Buttons[0][0].Payload != "cars:2" {
		t.Fatal("snapshot changed recorded callback")
	}
}
