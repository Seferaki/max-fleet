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
	if t == nil || t.messages == nil {
		return "", errors.New("MAX transport is missing")
	}
	if err := validateTextMessage(userID, text); err != nil {
		return "", err
	}
	result, err := t.messages.Send(ctx, maxbot.NewMessage().SetUser(userID).SetText(text))
	if err != nil {
		return "", err
	}
	if result.Message.Body.Mid == "" {
		return "", errors.New("MAX returned a message without an ID")
	}
	return result.Message.Body.Mid, nil
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
	UserID int64
	Text   string
	ID     string
}

// RecordingTransport is an in-memory transport for deterministic dialog tests.
type RecordingTransport struct {
	mu       sync.Mutex
	messages []RecordedText
}

func (t *RecordingTransport) SendText(ctx context.Context, userID int64, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateTextMessage(userID, text); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	id := fmt.Sprintf("recorded-%d", len(t.messages)+1)
	t.messages = append(t.messages, RecordedText{UserID: userID, Text: text, ID: id})
	return id, nil
}

func (t *RecordingTransport) Messages() []RecordedText {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RecordedText(nil), t.messages...)
}
