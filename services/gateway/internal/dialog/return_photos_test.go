package dialog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func TestReturnAfterPhotosEightConfirmAndErrors(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	draft := readyReturnDraft(t, actor, driver)
	fetcher := &syntheticPhotoFetcher{image: samplePhoto(t, 1)}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, MAX: sender, Photos: fetcher, PhotoStore: actor}
	worker := inboxworker.Worker{ID: "after-photo-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	deliver := func(event dataapi.NormalizedEvent) string {
		t.Helper()
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
			t.Fatalf("after photo event %s: %+v %v", event.EventKey, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1].Text
	}
	if got := deliver(photoItem(driver, "before-return-math", now).Event); !strings.Contains(got, "нет активного осмотра после") {
		t.Fatalf("early photo: %q", got)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Trip == nil || state.Return == nil || len(state.Return.Inspection.OccupiedSlots) != 0 {
		t.Fatalf("early photo changed draft: %+v %v", state, err)
	}
	created, err := actor.ChallengeCreateReturn(context.Background(), driver, draft.ID, draft.Version, state.Trip.ID, state.Trip.Version-1, "after-photo-challenge", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](created)
	if err != nil {
		t.Fatal(err)
	}
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	for index, value := range challenge.Options {
		if value == a+b {
			if _, err := actor.ChallengeAnswer(context.Background(), driver, challenge.ID, challenge.Version, index, "after-photo-answer", nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "after-photo-menu", now)); err != nil {
		t.Fatal(err)
	}
	var photoButton string
	for _, row := range sender.Messages()[len(sender.Messages())-1].Buttons {
		if strings.HasPrefix(row[0].Payload, "return-photos:") {
			photoButton = row[0].Payload
		}
	}
	if photoButton == "" {
		t.Fatal("after photo menu button missing")
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "after-photo-prompt", photoButton, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "1/8") {
		t.Fatalf("photo prompt: %v %+v", err, sender.Messages())
	}
	fetcher.err = maxsdk.ErrPhotoUnavailable
	if got := deliver(photoItem(driver, "after-photo-download-failed", now).Event); !strings.Contains(got, "Не удалось") {
		t.Fatalf("download failure: %q", got)
	}
	fetcher.err = nil
	if got := deliver(photoItem("8000000000000000002", "after-photo-foreign", now).Event); !strings.Contains(got, "нет активного шага загрузки фото") {
		t.Fatalf("foreign photo: %q", got)
	}
	if err := processor.Handle(context.Background(), photoItem(driver, "after-photo-no-lease", now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("unleased photo = %v", err)
	}
	for slot := 1; slot <= 8; slot++ {
		fetcher.image = samplePhoto(t, uint8(slot))
		if got := deliver(photoItem(driver, fmt.Sprintf("after-photo-%d", slot), now).Event); !strings.Contains(got, fmt.Sprintf("%d/8", slot)) {
			t.Fatalf("slot %d: %q", slot, got)
		}
		if slot == 1 {
			if got := deliver(photoItem(driver, "after-photo-duplicate", now).Event); !strings.Contains(got, "уже есть") {
				t.Fatalf("duplicate image: %q", got)
			}
		}
		if slot == 7 {
			state, err := actor.State(context.Background(), driver)
			if err != nil || state.Return == nil {
				t.Fatal(err)
			}
			payload := fmt.Sprintf("return-confirm-photos:%s:%d", state.Return.Inspection.ID, state.Return.Inspection.Version)
			if got := deliver(callbackItem(driver, "after-photo-early-confirm", payload, now).Event); !strings.Contains(got, "неполный") {
				t.Fatalf("early confirm: %q", got)
			}
		}
	}
	if got := deliver(photoItem(driver, "after-photo-ninth", now).Event); !strings.Contains(got, "Девятое фото не добавлено") {
		t.Fatalf("ninth photo: %q", got)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || len(state.Return.Inspection.OccupiedSlots) != 8 || state.Return.Inspection.PhotosConfirmedAt != nil {
		t.Fatalf("after set: %+v %v", state, err)
	}
	payload := fmt.Sprintf("return-confirm-photos:%s:%d", state.Return.Inspection.ID, state.Return.Inspection.Version)
	if got := deliver(callbackItem(driver, "after-photo-confirm", payload, now).Event); !strings.Contains(got, "подтверждены") {
		t.Fatalf("confirmation: %q", got)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Inspection.PhotosConfirmedAt == nil || len(state.Return.Inspection.OccupiedSlots) != 8 || state.Trip == nil || state.Trip.Status != "returning" {
		t.Fatalf("confirmed after photos: %+v %v", state, err)
	}
}
