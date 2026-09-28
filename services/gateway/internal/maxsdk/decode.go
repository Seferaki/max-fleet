package maxsdk

import (
	"encoding/json"

	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// DecodeUpdate reads the webhook wire shape into the same model.Update type
// returned by the pinned SDK's polling API. Only fields used by Normalize are
// copied; new MAX fields cannot silently become authorization inputs.
func DecodeUpdate(body []byte) (model.Update, error) {
	var raw struct {
		UpdateType model.UpdateType `json:"update_type"`
		Timestamp  int64            `json:"timestamp"`
		ChatID     int64            `json:"chat_id"`
		IsChannel  bool             `json:"is_channel"`
		User       model.User       `json:"user"`
		Message    model.Message    `json:"message"`
		Callback   model.Callback   `json:"callback"`
		Payload    string           `json:"payload"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return model.Update{}, ErrInvalidUpdate
	}
	update := model.Update{UpdateType: raw.UpdateType, Timestamp: raw.Timestamp, ChatID: raw.ChatID}
	switch raw.UpdateType {
	case model.UpdateBotStarted:
		update.IsChannel = raw.IsChannel
		update.User = &raw.User
		update.Payload = raw.Payload
	case model.UpdateMessageCreated:
		update.ChatID = raw.Message.Recipient.ChatID
		update.UserID = raw.Message.Sender.UserID
		update.MessageID = raw.Message.Body.Mid
		update.Message = messageFromRaw(raw.Message)
	case model.UpdateMessageCallback:
		update.ChatID = raw.Message.Recipient.ChatID
		update.UserID = raw.Message.Recipient.UserID
		update.MessageID = raw.Message.Body.Mid
		update.Message = messageFromRaw(raw.Message)
		update.Callback = &raw.Callback
	}
	return update, nil
}

func messageFromRaw(message model.Message) *model.MessageUpdate {
	return &model.MessageUpdate{
		Timestamp: message.Timestamp,
		Recipient: message.Recipient,
		Sender:    model.Sender{UserID: message.Sender.UserID, IsBot: message.Sender.IsBot},
		Body:      message.Body,
		Link:      message.Link,
	}
}
