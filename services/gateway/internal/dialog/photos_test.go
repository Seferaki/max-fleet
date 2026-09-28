package dialog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type photoReader struct {
	emptyCatalogReader
	state dataapi.CurrentState
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
