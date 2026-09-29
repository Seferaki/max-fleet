package dialog

import (
	"context"
	"errors"
	"fmt"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func issuePhotoDraft(state dataapi.CurrentState) (*dataapi.Conversation, bool) {
	checkout, draft := state.Checkout, state.Conversation
	if draft == nil || draft.Step != "collect_photos" || draft.Context.TargetID == nil || draft.Context.VehicleID == nil || draft.Context.DraftText == nil || draft.Context.IssueCategory == nil || len(draft.Context.AssetIDs) > 3 {
		return nil, false
	}
	if draft.Flow == "issue_during" {
		trip := state.Trip
		return draft, trip != nil && trip.Status == "active" && state.Return == nil && draft.Context.TripID != nil && *draft.Context.TripID == trip.ID && *draft.Context.TargetID == trip.ID && *draft.Context.VehicleID == trip.VehicleID
	}
	if draft.Flow == "issue_after" {
		trip, returnDraft := state.Trip, state.Return
		return draft, trip != nil && trip.Status == "returning" && returnDraft != nil && returnDraft.Status == "draft" && returnDraft.Step == "checklist" && returnDraft.Inspection.Status == "draft" && draft.Context.TripID != nil && *draft.Context.TripID == trip.ID && draft.Context.ReturnID != nil && *draft.Context.ReturnID == returnDraft.ID && *draft.Context.TargetID == returnDraft.Inspection.ID && *draft.Context.VehicleID == trip.VehicleID
	}
	if draft.Flow != "issue_before" || checkout == nil || checkout.Status != "holding" || checkout.Step != "inspection" || !inspectionReadyForIssueQuestion(checkout.Inspection) || checkout.Inspection.NewDamage == nil || !*checkout.Inspection.NewDamage || *draft.Context.TargetID != checkout.Inspection.ID || *draft.Context.VehicleID != checkout.VehicleID {
		return nil, false
	}
	return draft, true
}

func (p Bootstrap) issuePhotoHelp(ctx context.Context, maxID int64, state dataapi.CurrentState, inspectionID string, version int64) error {
	draft, ok := issuePhotoDraft(state)
	if !ok || draft.Context.TargetID == nil || *draft.Context.TargetID != inspectionID || draft.Version != version {
		return p.sendView(ctx, maxID, "Черновик замечания изменился или hold истёк. Обновите /menu.", nil)
	}
	if len(draft.Context.AssetIDs) == 3 {
		return p.sendView(ctx, maxID, "К замечанию уже сохранены 3/3 дополнительных фото. Четвёртое не добавится.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Дополнительные фото замечания: %d/3. Отправьте одно изображение JPEG, PNG или WebP. Эти фото не заменяют обязательные 8 ракурсов.", len(draft.Context.AssetIDs)), nil)
}

func (p Bootstrap) issuePhotoStage(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState) error {
	draft, ok := issuePhotoDraft(state)
	if !ok {
		return p.sendView(ctx, maxID, "Черновик замечания изменился или hold истёк. Обновите /menu.", nil)
	}
	count := len(draft.Context.AssetIDs)
	if draft.Context.Cursor != nil && *draft.Context.Cursor == item.Event.EventKey {
		return p.sendView(ctx, maxID, fmt.Sprintf("Это фото уже сохранено в черновике. Дополнительные фото: %d/3.", count), nil)
	}
	if count >= 3 {
		return p.sendView(ctx, maxID, "К замечанию уже сохранены 3/3 дополнительных фото. Четвёртое не добавлено.", nil)
	}
	if p.Photos == nil || p.PhotoStore == nil || p.Commands == nil {
		return inboxworker.ErrDeferred
	}
	if item.ID == "" || item.LeaseToken == "" || item.Event.EventKey == "" || item.Event.Payload.PhotoSourceKey == nil {
		return errors.New("issue photo requires durable inbox identity and source")
	}
	photo, err := p.Photos.Download(ctx, *item.Event.Payload.PhotoSourceKey)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := "Не удалось получить фото замечания. Отправьте одно изображение ещё раз."
		if errors.Is(err, maxsdk.ErrPhotoTooLarge) {
			message = "Фото больше 10 МБ. Отправьте изображение меньшего размера."
		} else if errors.Is(err, maxsdk.ErrPhotoFormat) {
			message = "Поддерживаются изображения JPEG, PNG или WebP."
		}
		return p.sendView(ctx, maxID, message, nil)
	}
	stageKey, err := inboxworker.CommandKey(item, "issue.asset.stage")
	if err != nil {
		return err
	}
	scopeType, employeeID := "inspection", ""
	if draft.Flow == "issue_during" {
		scopeType, employeeID = "trip", state.Trip.EmployeeID
	} else if draft.Flow == "issue_after" {
		employeeID = state.Trip.EmployeeID
	} else {
		employeeID = state.Checkout.EmployeeID
	}
	staged, err := p.PhotoStore.StageIssueAsset(ctx, actor, dataapi.IssueStageInput{ScopeType: scopeType, ScopeID: *draft.Context.TargetID, SourceEventKey: item.Event.EventKey, IdempotencyKey: stageKey, ContentType: photo.ContentType, Image: photo.Bytes})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 422 && apiErr.Code == "DUPLICATE_PHOTO" {
			return p.sendView(ctx, maxID, "Это фото уже есть в замечании. Отправьте другое изображение.", nil)
		}
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Черновик или hold уже недоступен. Обновите /menu.", nil)
		}
		return err
	}
	context := draft.Context
	context.AssetIDs = append(append([]string{}, draft.Context.AssetIDs...), staged.AssetID)
	context.Cursor = &item.Event.EventKey
	kind := "photo"
	saveKey, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employeeID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: draft.Flow, Step: "collect_photos", Context: context, PendingInputKind: &kind}, saveKey, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Черновик или hold уже недоступен. Обновите /menu.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Context.Cursor == nil || *saved.Context.Cursor != item.Event.EventKey || len(saved.Context.AssetIDs) != count+1 || saved.Context.AssetIDs[count] != staged.AssetID {
		return errors.New("issue photo returned invalid conversation")
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Дополнительное фото сохранено в черновике: %d/3. Замечание ещё не отправлено; откройте /menu.", count+1), nil)
}
