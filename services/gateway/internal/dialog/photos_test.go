package dialog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type photoReader struct {
	emptyCatalogReader
	state dataapi.CurrentState
}

type syntheticPhotoFetcher struct {
	image []byte
	err   error
}

type failOnePhotoReply struct {
	maxsdk.RecordingTransport
	fail bool
}

func (s *failOnePhotoReply) SendText(ctx context.Context, userID int64, message string) (string, error) {
	if s.fail {
		s.fail = false
		return "", errors.New("synthetic send interruption")
	}
	return s.RecordingTransport.SendText(ctx, userID, message)
}

func (f *syntheticPhotoFetcher) Download(context.Context, string) (maxsdk.DownloadedPhoto, error) {
	if f.err != nil {
		return maxsdk.DownloadedPhoto{}, f.err
	}
	return maxsdk.DownloadedPhoto{ContentType: "image/png", Bytes: f.image}, nil
}

func samplePhoto(t *testing.T, tone uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: tone, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func setupPhotoCheckout(t *testing.T, actor *dataapi.Client, driver string) dataapi.Checkout {
	t.Helper()
	ctx := context.Background()
	available := true
	page, err := actor.Vehicles(ctx, driver, dataapi.VehicleFilter{Available: &available, Limit: 5})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("vehicles: %v", err)
	}
	vehicle := page.Items[0]
	created, err := actor.CheckoutCreate(ctx, driver, vehicle.ID, vehicle.Version, "photo-setup-checkout", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	createdChallenge, err := actor.ChallengeCreateTake(ctx, driver, checkout.ID, checkout.Version, vehicle.ID, vehicle.Version, "photo-setup-math", nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](createdChallenge)
	if err != nil {
		t.Fatal(err)
	}
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatal(err)
	}
	correct := -1
	for i, answer := range challenge.Options {
		if answer == a+b {
			correct = i
		}
	}
	if correct < 0 {
		t.Fatal("math has no correct option")
	}
	if _, err := actor.ChallengeAnswer(ctx, driver, challenge.ID, challenge.Version, correct, "photo-setup-answer", nil); err != nil {
		t.Fatal(err)
	}
	state, err := actor.State(ctx, driver)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout state: %v", err)
	}
	rules, err := actor.CurrentRules(ctx, driver)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := actor.CheckoutAcceptRules(ctx, driver, state.Checkout.ID, state.Checkout.Version, rules.ID, "photo-setup-rules", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err = dataapi.DecodeAggregate[dataapi.Checkout](accepted)
	if err != nil || checkout.Step != "inspection" {
		t.Fatalf("inspection setup: %+v %v", checkout, err)
	}
	return checkout
}

func photoItem(actor, key string, now time.Time) dataapi.InboxClaimItem {
	source := "https://cdn.max.ru/synthetic-photo"
	return dataapi.InboxClaimItem{Event: dataapi.NormalizedEvent{
		IntegrationKey: "demo-bot", EventKey: "message:" + key + ":message_created", EventType: "message_created",
		ActorMaxUserID: actor, ChatID: actor, MessageID: &key, OccurredAt: now,
		Payload: dataapi.NormalizedPayload{Kind: "photo", PhotoSourceKey: &source, AttachmentCount: 1},
	}}
}

func TestPhotoUploadCountsOnlyStoredImageAndStopsAtEight(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClients(t, now)
	defer closeServer()
	const driver = "8000000000000000001"
	checkout := setupPhotoCheckout(t, actor, driver)
	fetcher := &syntheticPhotoFetcher{image: samplePhoto(t, 1)}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: actor, Commands: actor, PhotoStore: actor, Photos: fetcher, MAX: sender}
	worker := inboxworker.Worker{ID: "photo-worker", Store: store, Processor: processor, Now: func() time.Time { return now }}
	deliver := func(maxID, key string) string {
		t.Helper()
		event := photoItem(maxID, key, now).Event
		if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
			t.Fatal(err)
		}
		result, err := worker.RunOnce(context.Background(), 1)
		if err != nil || result.Acked != 1 {
			t.Fatalf("photo event %s: %+v %v", key, result, err)
		}
		messages := sender.Messages()
		return messages[len(messages)-1].Text
	}
	fetcher.err = maxsdk.ErrPhotoUnavailable
	if got := deliver(driver, "photo-failed-download"); !strings.Contains(got, "Не удалось") {
		t.Fatalf("download error: %q", got)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || len(state.Checkout.Inspection.OccupiedSlots) != 0 {
		t.Fatalf("failed download counted: %+v %v", state, err)
	}
	fetcher.err = nil
	if got := deliver("8000000000000000002", "photo-other-actor"); !strings.Contains(got, "нет активного") {
		t.Fatalf("other actor: %q", got)
	}
	if err := processor.Handle(context.Background(), photoItem(driver, "photo-without-lease", now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("missing lease: %v", err)
	}
	for slot := 1; slot <= 8; slot++ {
		fetcher.image = samplePhoto(t, uint8(slot))
		if got := deliver(driver, fmt.Sprintf("photo-slot-%d", slot)); !strings.Contains(got, fmt.Sprintf("%d/8", slot)) {
			t.Fatalf("slot %d: %q", slot, got)
		}
		if slot == 1 {
			if got := deliver(driver, "photo-duplicate-hash"); !strings.Contains(got, "уже есть") {
				t.Fatalf("duplicate hash: %q", got)
			}
		}
		if slot == 7 {
			state, err := actor.State(context.Background(), driver)
			if err != nil {
				t.Fatal(err)
			}
			payload := fmt.Sprintf("confirm-photos:%s:%d", state.Checkout.Inspection.ID, state.Checkout.Inspection.Version)
			event := callbackItem(driver, "photo-premature-confirm", payload, now).Event
			if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
				t.Fatal(err)
			}
			if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "Комплект неполный") {
				t.Fatalf("premature confirmation: %+v %v", result, err)
			}
		}
	}
	if got := deliver(driver, "photo-ninth"); !strings.Contains(got, "Девятое фото не добавлено") {
		t.Fatalf("ninth image: %q", got)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout == nil || state.Checkout.Inspection.ID != checkout.Inspection.ID || len(state.Checkout.Inspection.OccupiedSlots) != 8 {
		t.Fatalf("photo set: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "photo-confirm-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[len(sender.Messages())-1]
	if len(menu.Buttons) != 6 || !strings.HasPrefix(menu.Buttons[1][0].Payload, "confirm-photos:") {
		t.Fatalf("full photo menu: %+v", menu)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "photo-confirm-no-lease", menu.Buttons[1][0].Payload, now)); err == nil || !strings.Contains(err.Error(), "durable inbox lease") {
		t.Fatalf("confirmation without lease: %v", err)
	}
	confirm := callbackItem(driver, "photo-confirm", menu.Buttons[1][0].Payload, now).Event
	if _, err := store.StoreInbox(context.Background(), confirm, maxsdk.InboxIdempotencyKey(confirm)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "подтверждены") {
		t.Fatalf("confirmation: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.PhotosConfirmedAt == nil || len(state.Checkout.Inspection.OccupiedSlots) != 8 {
		t.Fatalf("confirmed set: %+v %v", state, err)
	}
	confirmedVersion := state.Checkout.Inspection.Version
	if err := processor.Handle(context.Background(), callbackItem(driver, "photo-stale-confirm", menu.Buttons[1][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "устарела") {
		t.Fatalf("stale confirmation: %v", err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.Version != confirmedVersion {
		t.Fatalf("stale confirmation changed state: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), menuItem(driver, "photo-replace-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu = sender.Messages()[len(sender.Messages())-1]
	if len(menu.Buttons) != 5 || !strings.HasPrefix(menu.Buttons[1][0].Payload, "replace-photos:") {
		t.Fatalf("replacement menu: %+v", menu)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "photo-replace-choose", menu.Buttons[1][0].Payload, now)); err != nil {
		t.Fatal(err)
	}
	choices := sender.Messages()[len(sender.Messages())-1]
	if len(choices.Buttons) != 8 || !strings.HasSuffix(choices.Buttons[2][0].Payload, ":3") {
		t.Fatalf("replacement choices: %+v", choices)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "photo-replace-slot-3", choices.Buttons[2][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "/replace 3") {
		t.Fatalf("replacement instruction: %v", err)
	}
	replacement := photoItem(driver, "photo-replacement", now).Event
	caption := "/replace 3"
	replacement.Payload.Text = &caption
	fetcher.err = maxsdk.ErrPhotoUnavailable
	failed := replacement
	failed.EventKey = "message:photo-replacement-failed:message_created"
	failedMessageID := "photo-replacement-failed"
	failed.MessageID = &failedMessageID
	if _, err := store.StoreInbox(context.Background(), failed, maxsdk.InboxIdempotencyKey(failed)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 {
		t.Fatalf("failed replacement: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.PhotosConfirmedAt == nil || state.Checkout.Inspection.Version != confirmedVersion {
		t.Fatalf("failed replacement changed set: %+v %v", state, err)
	}
	fetcher.err = nil
	fetcher.image = samplePhoto(t, 99)
	if _, err := store.StoreInbox(context.Background(), replacement, maxsdk.InboxIdempotencyKey(replacement)); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.RunOnce(context.Background(), 1); err != nil || result.Acked != 1 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "заменено") {
		t.Fatalf("replacement: %+v %v", result, err)
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || state.Checkout.Inspection.PhotosConfirmedAt != nil || state.Checkout.Inspection.Version != confirmedVersion+1 || len(state.Checkout.Inspection.OccupiedSlots) != 8 {
		t.Fatalf("replacement set: %+v %v", state, err)
	}
	if err := processor.Handle(context.Background(), callbackItem(driver, "photo-replace-stale", choices.Buttons[2][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "изменился") {
		t.Fatalf("stale replacement selection: %v", err)
	}
}

func TestPhotoUploadReplyInterruptionDoesNotCountSameEventTwice(t *testing.T) {
	current := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, store, closeServer := mockClientsClock(t, func() time.Time { return current })
	defer closeServer()
	const driver = "8000000000000000001"
	setupPhotoCheckout(t, actor, driver)
	sender := &failOnePhotoReply{fail: true}
	processor := Bootstrap{Data: actor, PhotoStore: actor, Photos: &syntheticPhotoFetcher{image: samplePhoto(t, 91)}, MAX: sender}
	worker := inboxworker.Worker{ID: "photo-retry-worker", Store: store, Processor: processor, Now: func() time.Time { return current }}
	event := photoItem(driver, "photo-interrupted-reply", current).Event
	if _, err := store.StoreInbox(context.Background(), event, maxsdk.InboxIdempotencyKey(event)); err != nil {
		t.Fatal(err)
	}
	first, err := worker.RunOnce(context.Background(), 1)
	if err != nil || first.Retried != 1 {
		t.Fatalf("first interrupted event: %+v %v", first, err)
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || len(state.Checkout.Inspection.OccupiedSlots) != 1 {
		t.Fatalf("upload before reply: %+v %v", state, err)
	}
	firstVersion := state.Checkout.Inspection.Version
	current = current.Add(6 * time.Second)
	second, err := worker.RunOnce(context.Background(), 1)
	if err != nil || second.Acked != 1 || len(sender.Messages()) != 1 || !strings.Contains(sender.Messages()[0].Text, "уже обрабатывалось") {
		t.Fatalf("retried event: %+v %v messages=%+v", second, err, sender.Messages())
	}
	state, err = actor.State(context.Background(), driver)
	if err != nil || len(state.Checkout.Inspection.OccupiedSlots) != 1 || state.Checkout.Inspection.Version != firstVersion {
		t.Fatalf("event counted twice: %+v %v", state, err)
	}
}

func (r photoReader) State(context.Context, string) (dataapi.CurrentState, error) {
	return r.state, nil
}

func TestPhotoPromptRestoresFirstMissingSlotAndRejectsStaleButton(t *testing.T) {
	const checkoutID = "40000000-0000-4000-8000-000000000001"
	checkout := &dataapi.Checkout{ID: checkoutID, Status: "holding", Step: "inspection", Version: 7,
		Inspection: dataapi.Inspection{ID: "40000000-0000-4000-8000-000000000002", Phase: "before", Status: "draft", OccupiedSlots: []int{1, 2, 4}, Version: 4}}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: photoReader{state: dataapi.CurrentState{Checkout: checkout}}, MAX: sender}
	actor := "8000000000000000001"
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	if err := processor.Handle(context.Background(), menuItem(actor, "photos-menu", now)); err != nil {
		t.Fatal(err)
	}
	menu := sender.Messages()[0]
	if !strings.Contains(menu.Text, "Сохранено 3/8") || !strings.Contains(menu.Text, "фото 3/8") || menu.Buttons[0][0].Payload != "photos:"+checkoutID+":7" {
		t.Fatalf("photo menu = %+v", menu)
	}
	if err := processor.Handle(context.Background(), callbackItem(actor, "photos-prompt", menu.Buttons[0][0].Payload, now)); err != nil {
		t.Fatal(err)
	}
	if got := sender.Messages()[1].Text; !strings.Contains(got, "Фото 3/8 — левый борт") || !strings.Contains(got, "одно изображение") {
		t.Fatalf("photo prompt = %q", got)
	}
	checkout.Version++
	if err := processor.Handle(context.Background(), callbackItem(actor, "photos-stale", menu.Buttons[0][0].Payload, now)); err != nil {
		t.Fatal(err)
	}
	if got := sender.Messages()[2].Text; !strings.Contains(got, "Шаг осмотра изменился") {
		t.Fatalf("stale photo button = %q", got)
	}
}

func TestPhotoPromptEightAndInvalidProjection(t *testing.T) {
	inspection := dataapi.Inspection{ID: "40000000-0000-4000-8000-000000000002", Phase: "before", Status: "draft", OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}}
	if slot, count, ok := photoSlot(inspection); !ok || slot != 0 || count != 8 || !strings.Contains(photoProgress(inspection), "Девятое фото") {
		t.Fatal("full photo set must not request a ninth slot")
	}
	inspection.OccupiedSlots = []int{1, 2, 2}
	if _, _, ok := photoSlot(inspection); ok {
		t.Fatal("duplicate slot in projection was accepted")
	}
	inspection.OccupiedSlots = []int{9}
	if _, _, ok := photoSlot(inspection); ok {
		t.Fatal("out-of-range slot in projection was accepted")
	}
}
