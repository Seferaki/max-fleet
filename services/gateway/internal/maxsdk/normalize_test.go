package maxsdk

import (
	"errors"
	"math"
	"testing"

	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

const maxTestTimestamp = 1790586000000

func directMessage(body model.MessageBody) model.Update {
	return model.Update{
		Timestamp:  maxTestTimestamp,
		ChatID:     123,
		UserID:     123,
		UpdateType: model.UpdateMessageCreated,
		Message: &model.MessageUpdate{
			Recipient: model.Recipient{ChatID: 123, ChatType: model.ChatTypeDialog},
			Sender:    model.Sender{UserID: 123},
			Body:      body,
		},
	}
}

func TestNormalizeMessageKinds(t *testing.T) {
	photo := directMessage(model.MessageBody{Mid: "photo-1", Attachments: []model.Attachment{{Type: model.AttachImage, Payload: model.Payload{URL: "https://cdn.example/photo-1"}}}})
	geo := directMessage(model.MessageBody{Mid: "geo-1", Attachments: []model.Attachment{{Type: model.AttachLocation, Latitude: 0, Longitude: 37.6}}})
	text := directMessage(model.MessageBody{Mid: "text-1", Text: "/menu"})
	for _, test := range []struct {
		update model.Update
		kind   string
		key    string
	}{
		{photo, "photo", "message:photo-1:message_created"},
		{geo, "geo", "message:geo-1:message_created"},
		{text, "text", "message:text-1:message_created"},
	} {
		event, err := Normalize("demo-bot", test.update)
		if err != nil || event.Payload.Kind != test.kind || event.EventKey != test.key || event.ActorMaxUserID != "123" || event.ChatID != "123" {
			t.Fatalf("normalized %s = %+v, %v", test.kind, event, err)
		}
		if event.MessageID == nil || *event.MessageID == "" || event.CallbackID != nil {
			t.Fatalf("message identity lost: %+v", event)
		}
	}
	event, _ := Normalize("demo-bot", photo)
	if event.Payload.PhotoSourceKey == nil || *event.Payload.PhotoSourceKey != "https://cdn.example/photo-1" || event.Payload.AttachmentCount != 1 {
		t.Fatalf("photo source lost: %+v", event.Payload)
	}
	captioned := directMessage(model.MessageBody{Mid: "photo-replace", Text: "/replace 3", Attachments: []model.Attachment{{Type: model.AttachImage, Payload: model.Payload{URL: "https://cdn.example/replacement"}}}})
	event, err := Normalize("demo-bot", captioned)
	if err != nil || event.Payload.Text == nil || *event.Payload.Text != "/replace 3" {
		t.Fatalf("photo caption lost: %+v %v", event.Payload, err)
	}
	event, _ = Normalize("demo-bot", geo)
	if event.Payload.Latitude == nil || *event.Payload.Latitude != 0 || event.Payload.Longitude == nil || *event.Payload.Longitude != 37.6 {
		t.Fatalf("zero latitude was lost: %+v", event.Payload)
	}
}

func TestNormalizeCallbackUsesClickerIdentity(t *testing.T) {
	update := model.Update{
		Timestamp:  maxTestTimestamp,
		ChatID:     123,
		UserID:     999, // SDK's callback path sets recipient user here, not the clicker.
		UpdateType: model.UpdateMessageCallback,
		Message: &model.MessageUpdate{
			Recipient: model.Recipient{ChatID: 123, UserID: 999, ChatType: model.ChatTypeDialog},
			Body:      model.MessageBody{Mid: "old-message"},
		},
		Callback: &model.Callback{CallbackID: "click-1", Payload: "vehicle:one", User: model.User{UserID: 123}},
	}
	event, err := Normalize("demo-bot", update)
	if err != nil || event.ActorMaxUserID != "123" || event.EventKey != "callback:click-1:message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || *event.Payload.CallbackData != "vehicle:one" {
		t.Fatalf("callback actor/payload = %+v, %v", event, err)
	}
	if event.MessageID != nil || event.CallbackID == nil || *event.CallbackID != "click-1" {
		t.Fatalf("callback ID = %+v", event)
	}
}

func TestNormalizeBotStartedFingerprint(t *testing.T) {
	update := model.Update{Timestamp: maxTestTimestamp, ChatID: 123, UpdateType: model.UpdateBotStarted, User: &model.User{UserID: 123}, Payload: "entry"}
	first, err := Normalize("demo-bot", update)
	if err != nil || first.Payload.Kind != "start" || first.ActorMaxUserID != "123" || first.MessageID != nil || first.CallbackID != nil {
		t.Fatalf("start = %+v, %v", first, err)
	}
	again, _ := Normalize("demo-bot", update)
	if first.EventKey != again.EventKey {
		t.Fatal("same start update changed its event key")
	}
	update.Payload = "other-entry"
	different, _ := Normalize("demo-bot", update)
	if first.EventKey == different.EventKey {
		t.Fatal("distinct starts share an event key")
	}
}

func TestNormalizeRejectsGroupAndAmbiguousMedia(t *testing.T) {
	group := directMessage(model.MessageBody{Mid: "group-1", Text: "hi"})
	group.Message.Recipient.ChatType = model.ChatTypeChat
	if _, err := Normalize("demo-bot", group); !errors.Is(err, ErrGroupEvent) {
		t.Fatalf("group message = %v", err)
	}
	multi := directMessage(model.MessageBody{Mid: "multi-1", Attachments: []model.Attachment{{Type: model.AttachImage}, {Type: model.AttachImage}}})
	partial, err := Normalize("demo-bot", multi)
	if !errors.Is(err, ErrMultipleAttachments) || partial.ActorMaxUserID == "" || partial.MessageID == nil || partial.Payload.AttachmentCount != 2 {
		t.Fatalf("two images = %+v, %v", partial, err)
	}
	unsupported := directMessage(model.MessageBody{Mid: "file-1", Attachments: []model.Attachment{{Type: model.AttachFile}}})
	partial, err = Normalize("demo-bot", unsupported)
	if !errors.Is(err, ErrUnsupportedContent) || partial.ActorMaxUserID == "" || partial.MessageID == nil || partial.Payload.PhotoSourceKey != nil || RejectionText(err) == "" {
		t.Fatalf("file = %+v, %v", partial, err)
	}
	invalidPhoto := directMessage(model.MessageBody{Mid: "photo-2", Attachments: []model.Attachment{{Type: model.AttachImage}}})
	if _, err := Normalize("demo-bot", invalidPhoto); !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("photo without source = %v", err)
	}
	badGeo := directMessage(model.MessageBody{Mid: "geo-2", Attachments: []model.Attachment{{Type: model.AttachLocation, Latitude: math.NaN()}}})
	if _, err := Normalize("demo-bot", badGeo); !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("invalid geo = %v", err)
	}
}

func TestNormalizeRejectsUnsupportedAndMissingIdentity(t *testing.T) {
	update := directMessage(model.MessageBody{Mid: "message-1", Text: "hello"})
	update.UpdateType = model.UpdateMessageEdited
	if _, err := Normalize("demo-bot", update); !errors.Is(err, ErrUnsupportedUpdate) {
		t.Fatalf("edited message = %v", err)
	}
	update = directMessage(model.MessageBody{Mid: "message-1", Text: "hello"})
	update.UserID = 0
	if _, err := Normalize("demo-bot", update); !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("missing actor = %v", err)
	}
	update = directMessage(model.MessageBody{Mid: "message-1", Text: "hello"})
	update.Message.Sender.IsBot = true
	if _, err := Normalize("demo-bot", update); !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("bot sender = %v", err)
	}
}
