package maxsdk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

var (
	ErrInvalidUpdate      = errors.New("invalid MAX update")
	ErrUnsupportedUpdate  = errors.New("unsupported MAX update")
	ErrGroupEvent         = errors.New("MAX group event")
	ErrUnsupportedContent = errors.New("unsupported MAX message content")
)

// Normalize converts the pinned SDK's update into the private DataAPI inbox
// schema. It never trusts a callback message recipient as the actor.
func Normalize(integrationKey string, update model.Update) (dataapi.NormalizedEvent, error) {
	switch update.UpdateType {
	case model.UpdateBotStarted, model.UpdateMessageCreated, model.UpdateMessageCallback:
	default:
		return dataapi.NormalizedEvent{}, ErrUnsupportedUpdate
	}
	if integrationKey == "" || len(integrationKey) > 100 || update.Timestamp <= 0 || update.ChatID <= 0 {
		return dataapi.NormalizedEvent{}, ErrInvalidUpdate
	}
	event := dataapi.NormalizedEvent{
		IntegrationKey: integrationKey,
		EventType:      string(update.UpdateType),
		ChatID:         strconv.FormatInt(update.ChatID, 10),
		OccurredAt:     time.UnixMilli(update.Timestamp).UTC(),
		Payload:        dataapi.NormalizedPayload{},
	}
	switch update.UpdateType {
	case model.UpdateBotStarted:
		if update.IsChannel || update.User == nil || update.User.UserID <= 0 || update.User.IsBot {
			return dataapi.NormalizedEvent{}, ErrGroupEvent
		}
		event.ActorMaxUserID = strconv.FormatInt(update.User.UserID, 10)
		event.Payload.Kind = "start"
		event.EventKey = fingerprint("start", integrationKey, event.ActorMaxUserID, event.ChatID, strconv.FormatInt(update.Timestamp, 10), update.Payload)
	case model.UpdateMessageCreated:
		if update.Message == nil || update.Message.Recipient.ChatType != model.ChatTypeDialog {
			return dataapi.NormalizedEvent{}, ErrGroupEvent
		}
		if update.UserID <= 0 || update.Message.Sender.IsBot || update.Message.Body.Mid == "" || len(update.Message.Body.Mid) > 160 {
			return dataapi.NormalizedEvent{}, ErrInvalidUpdate
		}
		event.ActorMaxUserID = strconv.FormatInt(update.UserID, 10)
		event.MessageID = stringPtr(update.Message.Body.Mid)
		event.EventKey = "message:" + update.Message.Body.Mid + ":message_created"
		if err := normalizeMessage(&event.Payload, update.Message.Body); err != nil {
			return dataapi.NormalizedEvent{}, err
		}
	case model.UpdateMessageCallback:
		if update.Message == nil || update.Message.Recipient.ChatType != model.ChatTypeDialog {
			return dataapi.NormalizedEvent{}, ErrGroupEvent
		}
		if update.Callback == nil || update.Callback.User.UserID <= 0 || update.Callback.User.IsBot || update.Callback.CallbackID == "" || len(update.Callback.CallbackID) > 155 || update.Callback.Payload == "" || utf8.RuneCountInString(update.Callback.Payload) > 200 {
			return dataapi.NormalizedEvent{}, ErrInvalidUpdate
		}
		event.ActorMaxUserID = strconv.FormatInt(update.Callback.User.UserID, 10)
		event.CallbackID = stringPtr(update.Callback.CallbackID)
		event.EventKey = "callback:" + update.Callback.CallbackID + ":message_callback"
		event.Payload.Kind = "callback"
		event.Payload.CallbackData = stringPtr(update.Callback.Payload)
		event.Payload.AttachmentCount = len(update.Message.Body.Attachments)
		if event.Payload.AttachmentCount > 100 {
			return dataapi.NormalizedEvent{}, ErrInvalidUpdate
		}
	default:
		return dataapi.NormalizedEvent{}, ErrUnsupportedUpdate
	}
	if len(event.EventKey) > 200 {
		return dataapi.NormalizedEvent{}, ErrInvalidUpdate
	}
	return event, nil
}

func normalizeMessage(payload *dataapi.NormalizedPayload, body model.MessageBody) error {
	payload.AttachmentCount = len(body.Attachments)
	if payload.AttachmentCount > 100 {
		return ErrInvalidUpdate
	}
	if payload.AttachmentCount > 1 {
		return ErrUnsupportedContent
	}
	if payload.AttachmentCount == 0 {
		if strings.TrimSpace(body.Text) == "" {
			return ErrUnsupportedContent
		}
		if !utf8.ValidString(body.Text) || utf8.RuneCountInString(body.Text) > 1000 {
			return ErrInvalidUpdate
		}
		payload.Kind = "text"
		payload.Text = stringPtr(body.Text)
		return nil
	}
	attachment := body.Attachments[0]
	switch attachment.Type {
	case model.AttachImage:
		source := attachment.Payload.URL
		if source == "" {
			source = attachment.Payload.Token
		}
		if source == "" || len(source) > 500 {
			return ErrInvalidUpdate
		}
		payload.Kind = "photo"
		payload.PhotoSourceKey = stringPtr(source)
	case model.AttachLocation:
		if !validCoordinate(attachment.Latitude, 90) || !validCoordinate(attachment.Longitude, 180) {
			return ErrInvalidUpdate
		}
		payload.Kind = "geo"
		payload.Latitude = &attachment.Latitude
		payload.Longitude = &attachment.Longitude
	default:
		return ErrUnsupportedContent
	}
	return nil
}

func validCoordinate(value, limit float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -limit && value <= limit
}

func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("fingerprint:%s", hex.EncodeToString(sum[:]))
}

func stringPtr(value string) *string { return &value }
