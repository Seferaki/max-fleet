package dialog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

var photoAngles = [8]string{
	"автомобиль спереди",
	"автомобиль сзади",
	"левый борт",
	"правый борт",
	"передняя часть салона",
	"задняя часть салона",
	"багажник",
	"приборная панель",
}

// The projection from Python is the only source of photo progress. An invalid
// projection must not lead to a guessed slot or a successful photo claim.
func photoSlot(inspection dataapi.Inspection) (int, int, bool) {
	if inspection.ID == "" || inspection.Phase != "before" || inspection.Status != "draft" {
		return 0, 0, false
	}
	var occupied [8]bool
	for _, slot := range inspection.OccupiedSlots {
		if slot < 1 || slot > 8 || occupied[slot-1] {
			return 0, 0, false
		}
		occupied[slot-1] = true
	}
	for i, taken := range occupied {
		if !taken {
			return i + 1, len(inspection.OccupiedSlots), true
		}
	}
	return 0, 8, true
}

func photoProgress(inspection dataapi.Inspection) string {
	slot, count, ok := photoSlot(inspection)
	if !ok {
		return "Состояние осмотра недоступно. Обновите /menu."
	}
	if slot == 0 {
		if inspection.PhotosConfirmedAt != nil {
			return "Фото 8/8 подтверждены."
		}
		return "Фото 8/8 сохранены. Девятое фото не добавляется; для изменения выберите замену ракурса."
	}
	return fmt.Sprintf("Сохранено %d/8. Следующее: фото %d/8 — %s.", count, slot, photoAngles[slot-1])
}

func (p Bootstrap) checkoutPhotos(ctx context.Context, maxID int64, state dataapi.CurrentState, checkoutID string, version int64) error {
	checkout := state.Checkout
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 || checkout == nil || checkout.ID != checkoutID || checkout.Version != version || checkout.Status != "holding" || checkout.Step != "inspection" {
		return p.sendView(ctx, maxID, "Шаг осмотра изменился или hold истёк. Обновите /menu.", nil)
	}
	slot, count, ok := photoSlot(checkout.Inspection)
	if !ok {
		return p.sendView(ctx, maxID, "Состояние осмотра недоступно. Обновите /menu.", nil)
	}
	if slot == 0 {
		if checkout.Inspection.PhotosConfirmedAt != nil {
			return p.sendView(ctx, maxID, "Фото 8/8 уже подтверждены.", nil)
		}
		return p.sendView(ctx, maxID, "Фото 8/8 уже сохранены. Девятое фото не добавится; далее можно подтвердить комплект или выбрать ракурс для замены.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Сохранено %d/8. Фото %d/8 — %s. Отправьте одно изображение для этого ракурса.", count, slot, photoAngles[slot-1]), nil)
}

func (p Bootstrap) confirmCheckoutPhotos(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, inspectionID string, version int64) error {
	checkout := state.Checkout
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || checkout == nil || checkout.Status != "holding" || checkout.Step != "inspection" || checkout.Inspection.ID != inspectionID || checkout.Inspection.Version != version {
		return p.sendView(ctx, maxID, "Кнопка подтверждения устарела или hold истёк. Обновите /menu.", nil)
	}
	slot, _, valid := photoSlot(checkout.Inspection)
	if !valid {
		return p.sendView(ctx, maxID, "Состояние осмотра недоступно. Обновите /menu.", nil)
	}
	if slot != 0 {
		return p.sendView(ctx, maxID, "Комплект неполный. "+photoProgress(checkout.Inspection), nil)
	}
	if checkout.Inspection.PhotosConfirmedAt != nil {
		return p.sendView(ctx, maxID, "Фото 8/8 уже подтверждены.", nil)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("photo confirmation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "inspection.confirm_photos")
	if err != nil {
		return err
	}
	result, err := p.Commands.InspectionConfirmPhotos(ctx, actor, inspectionID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 422 && apiErr.Code == "PHOTO_SET_INCOMPLETE" {
			return p.sendView(ctx, maxID, "Комплект неполный. Проверьте недостающие фото через /menu.", nil)
		}
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Осмотр изменился или hold истёк. Обновите /menu.", nil)
		}
		return err
	}
	confirmed, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil || confirmed.ID != inspectionID || confirmed.Version != version+1 || confirmed.PhotosConfirmedAt == nil || len(confirmed.OccupiedSlots) != 8 {
		return errors.New("photo confirmation returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Фото 8/8 подтверждены. Продолжите оформление через /menu.", nil)
}

func (p Bootstrap) checkoutPhotoUpload(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState) error {
	checkout := state.Checkout
	if checkout == nil || checkout.Status != "holding" || checkout.Step != "inspection" {
		return p.sendView(ctx, maxID, "Сейчас нет активного шага загрузки фото. Обновите /menu.", nil)
	}
	slot, count, ok := photoSlot(checkout.Inspection)
	if !ok {
		return p.sendView(ctx, maxID, "Состояние осмотра недоступно. Обновите /menu.", nil)
	}
	replacing := false
	if item.Event.Payload.Text != nil && strings.TrimSpace(*item.Event.Payload.Text) != "" {
		fields := strings.Fields(*item.Event.Payload.Text)
		if len(fields) != 2 || fields[0] != "/replace" {
			return p.sendView(ctx, maxID, "Для замены выберите ракурс через /menu и отправьте фото с подписью /replace N.", nil)
		}
		selected, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil || selected < 1 || selected > 8 || !containsPhotoSlot(checkout.Inspection.OccupiedSlots, selected) {
			return p.sendView(ctx, maxID, "Этот ракурс ещё не сохранён или номер неверен. Выберите сохранённый ракурс через /menu.", nil)
		}
		slot, replacing = selected, true
	}
	if slot == 0 && !replacing {
		return p.sendView(ctx, maxID, "Комплект 8/8 уже сохранён. Девятое фото не добавлено; выберите замену ракурса после открытия /menu.", nil)
	}
	if p.Photos == nil || p.PhotoStore == nil {
		return inboxworker.ErrDeferred
	}
	if item.LeaseToken == "" || item.ID == "" || item.Event.EventKey == "" {
		return errors.New("photo upload requires a durable inbox lease and event key")
	}
	photo, err := p.Photos.Download(ctx, *item.Event.Payload.PhotoSourceKey)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := "Не удалось получить фотографию. Отправьте одно изображение для текущего ракурса ещё раз."
		if errors.Is(err, maxsdk.ErrPhotoTooLarge) {
			message = "Фото больше 10 МБ. Отправьте изображение меньшего размера."
		} else if errors.Is(err, maxsdk.ErrPhotoFormat) {
			message = "Поддерживаются изображения JPEG, PNG или WebP. Отправьте другое фото."
		}
		return p.sendView(ctx, maxID, message, nil)
	}
	key, err := inboxworker.CommandKey(item, "inspection.photo")
	if err != nil {
		return err
	}
	result, err := p.PhotoStore.UploadInspectionPhoto(ctx, actor, dataapi.InspectionPhotoInput{
		InspectionID: checkout.Inspection.ID, Slot: slot, Version: checkout.Inspection.Version,
		SourceEventKey: item.Event.EventKey, IdempotencyKey: key,
		ContentType: photo.ContentType, Image: photo.Bytes,
	})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) {
			switch {
			case apiErr.Status == 422 && apiErr.Code == "DUPLICATE_PHOTO":
				return p.sendView(ctx, maxID, "Это изображение уже есть в осмотре. Отправьте другое фото для текущего ракурса.", nil)
			case apiErr.Status == 409 && apiErr.Code == "IDEMPOTENCY_CONFLICT":
				return p.sendView(ctx, maxID, "Это событие фото уже обрабатывалось. Проверьте текущий комплект через /menu; повторно оно не засчитано.", nil)
			case apiErr.Status == 409 || apiErr.Status == 404:
				return p.sendView(ctx, maxID, "Шаг осмотра изменился или hold истёк. Обновите /menu.", nil)
			}
		}
		return err
	}
	if result.Inspection.ID != checkout.Inspection.ID || result.Inspection.Version != checkout.Inspection.Version+1 {
		return errors.New("photo upload returned an unexpected inspection version")
	}
	_, saved, valid := photoSlot(result.Inspection)
	expectedCount := count + 1
	if replacing {
		expectedCount = count
	}
	if !valid || saved != expectedCount || !containsPhotoSlot(result.Inspection.OccupiedSlots, slot) || result.Inspection.PhotosConfirmedAt != nil {
		return errors.New("photo upload did not confirm the selected slot")
	}
	if replacing {
		return p.sendView(ctx, maxID, fmt.Sprintf("Фото %d/8 — %s заменено. Остальные ракурсы сохранены. %s", slot, photoAngles[slot-1], photoProgress(result.Inspection)), nil)
	}
	return p.sendView(ctx, maxID, photoProgress(result.Inspection), nil)
}

func replacePhotoSlotTarget(event dataapi.NormalizedEvent) (string, int64, int, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "replace-slot:") {
		return "", 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "replace-slot:"), ":")
	if len(parts) != 3 {
		return "", 0, 0, true
	}
	version, versionErr := strconv.ParseInt(parts[1], 10, 64)
	slot, slotErr := strconv.Atoi(parts[2])
	if versionErr != nil || slotErr != nil || version < 1 || slot < 1 || slot > 8 {
		return "", 0, 0, true
	}
	return parts[0], version, slot, true
}

func replacementCheckout(state dataapi.CurrentState, checkoutID string, version int64) (*dataapi.Checkout, bool) {
	checkout := state.Checkout
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 || checkout == nil || checkout.ID != checkoutID || checkout.Version != version || checkout.Status != "holding" || checkout.Step != "inspection" {
		return nil, false
	}
	_, _, ok := photoSlot(checkout.Inspection)
	return checkout, ok
}

func (p Bootstrap) chooseReplacement(ctx context.Context, maxID int64, state dataapi.CurrentState, checkoutID string, version int64) error {
	checkout, ok := replacementCheckout(state, checkoutID, version)
	if !ok {
		return p.sendView(ctx, maxID, "Выбор замены устарел или hold истёк. Обновите /menu.", nil)
	}
	rows := make([][]maxsdk.Button, 0, len(checkout.Inspection.OccupiedSlots))
	for _, slot := range checkout.Inspection.OccupiedSlots {
		rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("%d. %s", slot, photoAngles[slot-1]), Payload: fmt.Sprintf("replace-slot:%s:%d:%d", checkoutID, version, slot)}})
	}
	if len(rows) == 0 {
		return p.sendView(ctx, maxID, "Сохранённых фото для замены пока нет.", nil)
	}
	return p.sendView(ctx, maxID, "Выберите ракурс для замены. Старое фото сохранится, пока новое не примет хранилище.", rows)
}

func (p Bootstrap) requestReplacement(ctx context.Context, maxID int64, state dataapi.CurrentState, checkoutID string, version int64, slot int) error {
	checkout, ok := replacementCheckout(state, checkoutID, version)
	if !ok || slot < 1 || slot > 8 || !containsPhotoSlot(checkout.Inspection.OccupiedSlots, slot) {
		return p.sendView(ctx, maxID, "Ракурс для замены изменился или hold истёк. Обновите /menu.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Заменить фото %d/8 — %s. Пришлите одно изображение с подписью /replace %d. Старое фото останется до успешной загрузки нового.", slot, photoAngles[slot-1], slot), nil)
}

func containsPhotoSlot(slots []int, selected int) bool {
	for _, slot := range slots {
		if slot == selected {
			return true
		}
	}
	return false
}
