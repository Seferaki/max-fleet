package maxsdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// Transport keeps the dialog layer independent from the MAX SDK and network.
// A successful call returns MAX's message ID for delivery accounting.
type Transport interface {
	SendText(ctx context.Context, userID int64, text string) (string, error)
	SendButtons(ctx context.Context, userID int64, text string, rows [][]Button) (string, error)
}

type Button struct {
	Text    string
	Payload string
}

type messageSender interface {
	Send(context.Context, *maxbot.Message) (model.SendMessageResult, error)
}

type SDKTransport struct {
	messages messageSender
}

func NewTransport(api *maxbot.Api) (*SDKTransport, error) {
	if api == nil || api.Messages == nil {
		return nil, errors.New("MAX messages client is missing")
	}
	return &SDKTransport{messages: api.Messages}, nil
}

func (t *SDKTransport) SendText(ctx context.Context, userID int64, text string) (string, error) {
	return t.send(ctx, userID, text, nil)
}

func (t *SDKTransport) SendButtons(ctx context.Context, userID int64, text string, rows [][]Button) (string, error) {
	if err := validateButtons(rows); err != nil {
		return "", err
	}
	keyboard := model.NewKeyboard()
	for _, row := range rows {
		keyboardRow := keyboard.AddRow()
		for _, button := range row {
			keyboardRow.AddCallBack(button.Text, button.Payload)
		}
	}
	return t.send(ctx, userID, text, keyboard)
}

func (t *SDKTransport) send(ctx context.Context, userID int64, text string, keyboard *model.Keyboard) (string, error) {
	if t == nil || t.messages == nil {
		return "", errors.New("MAX transport is missing")
	}
	if err := validateTextMessage(userID, text); err != nil {
		return "", err
	}
	message := maxbot.NewMessage().SetUser(userID).SetText(text)
	if keyboard != nil {
		message.AddKeyboard(keyboard)
	}
	result, err := t.messages.Send(ctx, message)
	if err != nil {
		return "", err
	}
	if result.Message.Body.Mid == "" {
		return "", errors.New("MAX returned a message without an ID")
	}
	return result.Message.Body.Mid, nil
}

func validateButtons(rows [][]Button) error {
	if len(rows) < 1 || len(rows) > 10 {
		return errors.New("MAX keyboard row count is invalid")
	}
	for _, row := range rows {
		if len(row) < 1 || len(row) > 5 {
			return errors.New("MAX keyboard column count is invalid")
		}
		for _, button := range row {
			if strings.TrimSpace(button.Text) == "" || !utf8.ValidString(button.Text) || utf8.RuneCountInString(button.Text) > 100 || button.Payload == "" || !utf8.ValidString(button.Payload) || utf8.RuneCountInString(button.Payload) > 200 {
				return errors.New("MAX callback button is invalid")
			}
		}
	}
	return nil
}

func validateTextMessage(userID int64, text string) error {
	if userID <= 0 {
		return errors.New("MAX recipient ID must be positive")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("MAX message text is empty")
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 4000 {
		return errors.New("MAX message text is invalid or too long")
	}
	return nil
}

type RecordedText struct {
	UserID  int64
	Text    string
	ID      string
	Buttons [][]Button
}

// RecordingTransport is an in-memory transport for deterministic dialog tests.
type RecordingTransport struct {
	mu       sync.Mutex
	messages []RecordedText
}

func (t *RecordingTransport) SendText(ctx context.Context, userID int64, text string) (string, error) {
	return t.record(ctx, userID, text, nil)
}

func (t *RecordingTransport) SendButtons(ctx context.Context, userID int64, text string, rows [][]Button) (string, error) {
	if err := validateButtons(rows); err != nil {
		return "", err
	}
	return t.record(ctx, userID, text, rows)
}

func (t *RecordingTransport) record(ctx context.Context, userID int64, text string, rows [][]Button) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateTextMessage(userID, text); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	id := fmt.Sprintf("recorded-%d", len(t.messages)+1)
	t.messages = append(t.messages, RecordedText{UserID: userID, Text: text, ID: id, Buttons: copyButtons(rows)})
	return id, nil
}

func (t *RecordingTransport) Messages() []RecordedText {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := append([]RecordedText(nil), t.messages...)
	for i := range result {
		result[i].Buttons = copyButtons(result[i].Buttons)
	}
	return result
}

func copyButtons(rows [][]Button) [][]Button {
	if rows == nil {
		return nil
	}
	result := make([][]Button, len(rows))
	for i, row := range rows {
		result[i] = append([]Button(nil), row...)
	}
	return result
}
