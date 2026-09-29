package dialog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func returnPhotoDraft(state dataapi.CurrentState, employee dataapi.Employee) (*dataapi.Return, bool) {
	if !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" {
		return nil, false
	}
	_, _, ok := photoSlotPhase(state.Return.Inspection, "after")
	return state.Return, ok
}

func (p Bootstrap) returnPhotos(ctx context.Context, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string, version int64) error {
	draft, ok := returnPhotoDraft(state, employee)
	if !ok || !vehicleIDPattern.MatchString(returnID) || version < 1 || draft.ID != returnID || draft.Version != version {
		return p.sendView(ctx, maxID, "Шаг фото после поездки изменился. Обновите /menu.", nil)
	}
	slot, count, _ := photoSlotPhase(draft.Inspection, "after")
	if slot == 0 {
		if draft.Inspection.PhotosConfirmedAt != nil {
			return p.sendView(ctx, maxID, "Фото после поездки 8/8 уже подтверждены.", nil)
		}
		return p.sendView(ctx, maxID, "Фото после поездки 8/8 сохранены. Подтвердите комплект через /menu; девятое фото не добавляется.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Фото после поездки: сохранено %d/8. Отправьте одно изображение для ракурса %d/8 — %s.", count, slot, photoAngles[slot-1]), nil)
}

func (p Bootstrap) confirmReturnPhotos(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64) error {
	draft, ok := returnPhotoDraft(state, employee)
	if !ok || !vehicleIDPattern.MatchString(inspectionID) || version < 1 || draft.Inspection.ID != inspectionID || draft.Inspection.Version != version {
		return p.sendView(ctx, maxID, "Кнопка подтверждения фото после устарела. Обновите /menu.", nil)
	}
	slot, _, _ := photoSlotPhase(draft.Inspection, "after")
	if slot != 0 {
		return p.sendView(ctx, maxID, "Комплект фото после неполный. "+photoProgressPhase(draft.Inspection, "after"), nil)
	}
	if draft.Inspection.PhotosConfirmedAt != nil {
		return p.sendView(ctx, maxID, "Фото после поездки 8/8 уже подтверждены.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("after photo confirmation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "inspection.confirm_photos")
	if err != nil {
		return err
	}
	result, err := p.Commands.InspectionConfirmPhotos(ctx, actor, inspectionID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 422 && apiErr.Code == "PHOTO_SET_INCOMPLETE" {
			return p.sendView(ctx, maxID, "Комплект фото после неполный. Проверьте ракурсы через /menu.", nil)
		}
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Осмотр после изменился. Обновите /menu.", nil)
		}
		return err
	}
	confirmed, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil || confirmed.ID != inspectionID || confirmed.Phase != "after" || confirmed.Version != version+1 || confirmed.PhotosConfirmedAt == nil || len(confirmed.OccupiedSlots) != 8 || len(confirmed.MissingSlots) != 0 {
		return errors.New("after photo confirmation returned invalid inspection")
	}
	return p.sendView(ctx, maxID, "Фото после поездки 8/8 подтверждены. Продолжите возврат через /menu.", nil)
}

func (p Bootstrap) returnPhotoUpload(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState) error {
	draft, ok := returnPhotoDraft(state, employee)
	if !ok {
		return p.sendView(ctx, maxID, "Сейчас нет активного осмотра после поездки. Обновите /menu.", nil)
	}
	slot, count, _ := photoSlotPhase(draft.Inspection, "after")
	if item.Event.Payload.Text != nil && strings.TrimSpace(*item.Event.Payload.Text) != "" {
		return p.sendView(ctx, maxID, "Подпись к фото сейчас не нужна. Отправьте одно изображение для следующего ракурса.", nil)
	}
	if slot == 0 {
		return p.sendView(ctx, maxID, "Фото после поездки 8/8 уже сохранены. Девятое фото не добавлено; подтвердите комплект через /menu.", nil)
	}
	if p.Photos == nil || p.PhotoStore == nil {
		return inboxworker.ErrDeferred
	}
	if item.ID == "" || item.LeaseToken == "" || item.Event.EventKey == "" || item.Event.Payload.PhotoSourceKey == nil {
		return errors.New("after photo upload requires a durable inbox lease and event key")
	}
	photo, err := p.Photos.Download(ctx, *item.Event.Payload.PhotoSourceKey)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := "Не удалось получить фотографию. Отправьте одно изображение ещё раз."
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
	result, err := p.PhotoStore.UploadInspectionPhoto(ctx, actor, dataapi.InspectionPhotoInput{InspectionID: draft.Inspection.ID, Slot: slot, Version: draft.Inspection.Version, SourceEventKey: item.Event.EventKey, IdempotencyKey: key, ContentType: photo.ContentType, Image: photo.Bytes})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) {
			switch {
			case apiErr.Status == 422 && apiErr.Code == "DUPLICATE_PHOTO":
				return p.sendView(ctx, maxID, "Это изображение уже есть в осмотре. Отправьте другое фото.", nil)
			case apiErr.Status == 409 && apiErr.Code == "IDEMPOTENCY_CONFLICT":
				return p.sendView(ctx, maxID, "Это фото-событие уже обрабатывалось. Проверьте комплект через /menu; повторно оно не засчитано.", nil)
			case apiErr.Status == 409 || apiErr.Status == 404:
				return p.sendView(ctx, maxID, "Осмотр после изменился. Обновите /menu.", nil)
			}
		}
		return err
	}
	_, saved, valid := photoSlotPhase(result.Inspection, "after")
	if result.Inspection.ID != draft.Inspection.ID || result.Inspection.Version != draft.Inspection.Version+1 || !valid || saved != count+1 || !containsPhotoSlot(result.Inspection.OccupiedSlots, slot) || result.Inspection.PhotosConfirmedAt != nil {
		return errors.New("after photo upload returned invalid inspection")
	}
	return p.sendView(ctx, maxID, photoProgressPhase(result.Inspection, "after"), nil)
}
