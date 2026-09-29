package dialog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

const postReturnIssueFlow = "issue_post_return"

func postReturnIssueCategoryTarget(event dataapi.NormalizedEvent) (string, int64, string, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return "", 0, "", false
	}
	value := *event.Payload.CallbackData
	if !strings.HasPrefix(value, "trip-post-issue-kind:") {
		return "", 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(value, "trip-post-issue-kind:"), ":")
	if len(parts) != 3 {
		return "", 0, "", true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, "", true
	}
	return parts[0], version, parts[2], true
}

func validPostReturnIssueDraft(draft *dataapi.Conversation, tripID string) bool {
	if draft == nil || draft.Flow != postReturnIssueFlow || draft.Context.TargetID == nil || *draft.Context.TargetID != tripID || draft.Context.TripID == nil || *draft.Context.TripID != tripID || draft.Context.VehicleID == nil || !vehicleIDPattern.MatchString(*draft.Context.VehicleID) || draft.Context.VehicleVersion == nil || *draft.Context.VehicleVersion < 1 || draft.Context.IssueCategory == nil || draft.Context.IssueID != nil || len(draft.Context.AssetIDs) > 3 {
		return false
	}
	if _, _, ok := issueCategoryAlias(*draft.Context.IssueCategory); !ok {
		return false
	}
	if draft.Step == "awaiting_description" {
		return draft.Context.DraftText == nil && len(draft.Context.AssetIDs) == 0
	}
	return (draft.Step == "collect_photos" || draft.Step == "review") && draft.Context.DraftText != nil && strings.TrimSpace(*draft.Context.DraftText) != "" && utf8.RuneCountInString(*draft.Context.DraftText) <= 1000
}

func (p Bootstrap) ownedCompletedTrip(ctx context.Context, actor string, employee dataapi.Employee, tripID string, expectedVersion int64) (dataapi.Trip, bool, error) {
	reader, ok := p.Data.(interface {
		Trip(context.Context, string, string) (dataapi.Trip, error)
	})
	if !ok {
		return dataapi.Trip{}, false, errors.New("post-return issue trip reader is not configured")
	}
	if !vehicleIDPattern.MatchString(tripID) || expectedVersion < 0 {
		return dataapi.Trip{}, false, nil
	}
	trip, err := reader.Trip(ctx, actor, tripID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return dataapi.Trip{}, false, nil
		}
		return dataapi.Trip{}, false, err
	}
	if trip.ID != tripID || trip.EmployeeID != employee.ID || trip.Status != "completed" || expectedVersion > 0 && trip.Version != expectedVersion {
		return dataapi.Trip{}, false, nil
	}
	return trip, true, nil
}

func (p Bootstrap) startPostReturnIssue(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, tripID string, version int64) error {
	trip, ok, err := p.ownedCompletedTrip(ctx, actor, employee, tripID, version)
	if err != nil {
		return err
	}
	if !ok {
		return p.sendView(ctx, maxID, "Поездка недоступна или изменилась. Откройте /trips.", nil)
	}
	rows := make([][]maxsdk.Button, 0, len(issueCategories))
	for _, category := range issueCategories {
		rows = append(rows, []maxsdk.Button{{Text: category.label, Payload: fmt.Sprintf("trip-post-issue-kind:%s:%d:%s", trip.ID, trip.Version, category.code)}})
	}
	return p.sendView(ctx, maxID, "Сообщить о проблеме после поездки. Выберите категорию. Сообщение добавится отдельно и не изменит завершённый осмотр.", rows)
}

func (p Bootstrap) selectPostReturnIssueCategory(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, category string) error {
	trip, ok, err := p.ownedCompletedTrip(ctx, actor, employee, tripID, version)
	if err != nil {
		return err
	}
	_, _, categoryOK := issueCategoryAlias(category)
	if !ok || !categoryOK {
		return p.sendView(ctx, maxID, "Поездка или категория изменились. Откройте /trips.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("post-return issue category requires a durable inbox lease")
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, trip.VehicleID)
	if err != nil {
		return err
	}
	if vehicle.Status == "holding" || vehicle.Status == "in_trip" {
		return p.sendView(ctx, maxID, "Машина уже оформляется или находится в поездке. Сообщение после поездки сейчас нельзя безопасно отправить.", nil)
	}
	categoryCode, _, _ := issueCategoryAlias(category)
	kind := "text"
	context := dataapi.ConversationContext{TargetID: &trip.ID, TripID: &trip.ID, VehicleID: &trip.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &categoryCode}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: postReturnIssueFlow, Step: "awaiting_description", PendingInputKind: &kind, Context: context}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
			return p.sendView(ctx, maxID, "Поездка или черновик изменились. Откройте /trips и начните снова.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Flow != postReturnIssueFlow || saved.Step != "awaiting_description" || saved.Version != state.ConversationVersion+1 || saved.Context.TripID == nil || *saved.Context.TripID != trip.ID || saved.Context.IssueCategory == nil || *saved.Context.IssueCategory != categoryCode {
		return errors.New("post-return issue category returned invalid conversation")
	}
	return p.sendView(ctx, maxID, "Категория выбрана. Опишите проблему после поездки командой /issue <описание>. Она сохранится как отдельное замечание к поездке.", nil)
}

func (p Bootstrap) postReturnIssueDraft(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, input string) error {
	draft := state.Conversation
	if draft == nil || draft.Context.TripID == nil || !validPostReturnIssueDraft(draft, *draft.Context.TripID) {
		return p.sendView(ctx, maxID, "Черновик сообщения после поездки недоступен. Откройте нужную поездку в /trips.", nil)
	}
	trip, ok, err := p.ownedCompletedTrip(ctx, actor, employee, *draft.Context.TripID, 0)
	if err != nil {
		return err
	}
	if !ok || trip.VehicleID != *draft.Context.VehicleID {
		return p.sendView(ctx, maxID, "Поездка недоступна. Откройте /trips.", nil)
	}
	return p.postReturnIssueDraftWithTrip(ctx, item, actor, maxID, employee, state, input, draft)
}

func (p Bootstrap) postReturnIssueDraftWithTrip(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, input string, draft *dataapi.Conversation) error {
	if draft.Step == "collect_photos" {
		fields := strings.Fields(input)
		if len(fields) > 1 && strings.Join(fields[1:], " ") == *draft.Context.DraftText {
			return p.sendView(ctx, maxID, "Описание уже сохранено. Откройте /menu для фото и отправки.", nil)
		}
	}
	if draft.Step != "awaiting_description" {
		return p.sendView(ctx, maxID, "Черновик уже сохранён. Откройте /menu для фото и отправки.", nil)
	}
	fields := strings.Fields(input)
	if len(fields) < 2 {
		return p.sendView(ctx, maxID, "Формат: /issue <описание проблемы после поездки>.", nil)
	}
	description := strings.Join(fields[1:], " ")
	if utf8.RuneCountInString(description) > 1000 || strings.TrimSpace(description) == "" {
		return p.sendView(ctx, maxID, "Описание должно содержать не более 1000 знаков.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("post-return issue draft requires a durable inbox lease")
	}
	context := draft.Context
	context.DraftText = &description
	kind := "photo"
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: postReturnIssueFlow, Step: "collect_photos", PendingInputKind: &kind, Context: context}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
			return p.sendView(ctx, maxID, "Черновик или поездка изменились. Откройте /trips и начните снова.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Flow != postReturnIssueFlow || saved.Step != "collect_photos" || saved.Context.DraftText == nil || *saved.Context.DraftText != description {
		return errors.New("post-return issue draft returned invalid conversation")
	}
	return p.sendView(ctx, maxID, "Черновик сохранён. Дополнительные фото необязательны; замечание ещё не отправлено. Откройте /menu для проверки и отправки.", nil)
}

func (p Bootstrap) postReturnIssuePhotoHelp(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64) error {
	draft := state.Conversation
	if !validPostReturnIssueDraft(draft, tripID) || draft.Version != version || draft.Step != "collect_photos" {
		return p.sendView(ctx, maxID, "Черновик замечания изменился. Откройте /menu.", nil)
	}
	if _, ok, err := p.ownedCompletedTrip(ctx, actor, employee, tripID, 0); err != nil {
		return err
	} else if !ok {
		return p.sendView(ctx, maxID, "Поездка недоступна. Откройте /trips.", nil)
	}
	if len(draft.Context.AssetIDs) >= 3 {
		return p.sendView(ctx, maxID, "К замечанию уже сохранены 3/3 дополнительных фото.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Дополнительные фото: %d/3. Отправьте JPEG, PNG или WebP; фото необязательны и не заменяют 8 ракурсов осмотра.", len(draft.Context.AssetIDs)), nil)
}

func (p Bootstrap) postReturnIssuePhotoStage(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState) error {
	draft := state.Conversation
	if draft == nil || draft.Context.TripID == nil || !validPostReturnIssueDraft(draft, *draft.Context.TripID) || draft.Step != "collect_photos" {
		return p.sendView(ctx, maxID, "Черновик замечания изменился. Откройте /menu.", nil)
	}
	tripID := *draft.Context.TripID
	if draft.Context.Cursor != nil && *draft.Context.Cursor == item.Event.EventKey {
		return p.sendView(ctx, maxID, fmt.Sprintf("Это фото уже сохранено. Дополнительные фото: %d/3.", len(draft.Context.AssetIDs)), nil)
	}
	if len(draft.Context.AssetIDs) >= 3 {
		return p.sendView(ctx, maxID, "К замечанию уже сохранены 3/3 дополнительных фото. Четвёртое не добавлено.", nil)
	}
	if _, ok, err := p.ownedCompletedTrip(ctx, actor, employee, tripID, 0); err != nil {
		return err
	} else if !ok {
		return p.sendView(ctx, maxID, "Поездка недоступна. Откройте /trips.", nil)
	}
	if p.Photos == nil || p.PhotoStore == nil || p.Commands == nil {
		return inboxworker.ErrDeferred
	}
	if item.ID == "" || item.LeaseToken == "" || item.Event.EventKey == "" || item.Event.Payload.PhotoSourceKey == nil {
		return errors.New("post-return issue photo requires durable inbox identity and source")
	}
	photo, err := p.Photos.Download(ctx, *item.Event.Payload.PhotoSourceKey)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := "Не удалось получить фото замечания. Отправьте изображение ещё раз."
		if errors.Is(err, maxsdk.ErrPhotoTooLarge) {
			message = "Фото больше 10 МБ. Отправьте изображение меньшего размера."
		} else if errors.Is(err, maxsdk.ErrPhotoFormat) {
			message = "Поддерживаются JPEG, PNG или WebP."
		}
		return p.sendView(ctx, maxID, message, nil)
	}
	stageKey, err := inboxworker.CommandKey(item, "issue.asset.stage")
	if err != nil {
		return err
	}
	staged, err := p.PhotoStore.StageIssueAsset(ctx, actor, dataapi.IssueStageInput{ScopeType: "trip", ScopeID: tripID, SourceEventKey: item.Event.EventKey, IdempotencyKey: stageKey, ContentType: photo.ContentType, Image: photo.Bytes})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 422 && apiErr.Code == "DUPLICATE_PHOTO" {
			return p.sendView(ctx, maxID, "Это фото уже есть в замечании. Отправьте другое изображение.", nil)
		}
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Поездка или доступ к фото изменились. Откройте /trips.", nil)
		}
		return err
	}
	context := draft.Context
	context.AssetIDs = append(append([]string(nil), draft.Context.AssetIDs...), staged.AssetID)
	context.Cursor = &item.Event.EventKey
	kind := "photo"
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: postReturnIssueFlow, Step: "collect_photos", PendingInputKind: &kind, Context: context}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
			return p.sendView(ctx, maxID, "Черновик изменился. Откройте /menu перед повторной отправкой фото.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Flow != postReturnIssueFlow || saved.Context.Cursor == nil || *saved.Context.Cursor != item.Event.EventKey || len(saved.Context.AssetIDs) != len(draft.Context.AssetIDs)+1 || saved.Context.AssetIDs[len(draft.Context.AssetIDs)] != staged.AssetID {
		return errors.New("post-return issue photo returned invalid conversation")
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Дополнительное фото сохранено: %d/3. Замечание ещё не отправлено.", len(saved.Context.AssetIDs)), nil)
}

func (p Bootstrap) postReturnIssueSubmit(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, submit bool) error {
	draft := state.Conversation
	if draft != nil && draft.Flow == postReturnIssueFlow && draft.Step == "done" && draft.Context.TripID != nil && *draft.Context.TripID == tripID {
		if _, owned, err := p.ownedCompletedTrip(ctx, actor, employee, tripID, 0); err != nil {
			return err
		} else if !owned {
			return p.sendView(ctx, maxID, "Поездка недоступна. Откройте /trips.", nil)
		}
		return p.savedPostReturnIssue(ctx, actor, employee, maxID, draft)
	}
	if !vehicleIDPattern.MatchString(tripID) || version < 1 || !validPostReturnIssueDraft(draft, tripID) || draft.Version != version || draft.Step != "collect_photos" {
		return p.sendView(ctx, maxID, "Черновик замечания изменился. Откройте /menu.", nil)
	}
	trip, ok, err := p.ownedCompletedTrip(ctx, actor, employee, tripID, 0)
	if err != nil {
		return err
	}
	if !ok {
		return p.sendView(ctx, maxID, "Поездка недоступна. Откройте /trips.", nil)
	}
	if !submit {
		vehicle, err := p.Data.Vehicle(ctx, actor, trip.VehicleID)
		if err != nil {
			return err
		}
		if vehicle.Version != *draft.Context.VehicleVersion || vehicle.Status == "holding" || vehicle.Status == "in_trip" {
			return p.sendView(ctx, maxID, "Состояние машины изменилось после создания черновика. Обновите поездки и свяжитесь с ответственным, если проблема сохраняется.", nil)
		}
		_, label, ok := issueCategoryAlias(*draft.Context.IssueCategory)
		if !ok {
			return errors.New("post-return issue draft has invalid category")
		}
		message := fmt.Sprintf("Сообщение после поездки · %s\nКатегория: %s\nОписание: %s\nДополнительные фото: %d/3\nПосле отправки машина останется недоступной до проверки ответственного. Завершённый осмотр не изменится.", oneLine(vehicle.Plate), label, oneLine(*draft.Context.DraftText), len(draft.Context.AssetIDs))
		rows := [][]maxsdk.Button{{{Text: "Отправить сообщение", Payload: fmt.Sprintf("trip-post-issue-submit:%s:%d", tripID, version)}}, {{Text: "Добавить фото", Payload: fmt.Sprintf("trip-post-issue-photos:%s:%d", tripID, version)}}}
		return p.sendView(ctx, maxID, message, rows)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("post-return issue submit requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "issue.create")
	if err != nil {
		return err
	}
	input := dataapi.IssueCreateInput{Category: *draft.Context.IssueCategory, Description: *draft.Context.DraftText, TripID: &tripID, AssetIDs: append([]string(nil), draft.Context.AssetIDs...)}
	lease := &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}
	commandReader, ok := p.Data.(interface {
		OwnCommandResult(context.Context, string, string, string) (dataapi.CommandResult, error)
	})
	if !ok {
		return errors.New("post-return issue recovery reader is not configured")
	}
	result, err := commandReader.OwnCommandResult(ctx, actor, key, "issue.create")
	if err != nil {
		var apiErr *dataapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			return err
		}
		vehicle, readErr := p.Data.Vehicle(ctx, actor, trip.VehicleID)
		if readErr != nil {
			return readErr
		}
		if vehicle.Version != *draft.Context.VehicleVersion || vehicle.Status == "holding" || vehicle.Status == "in_trip" {
			return p.sendView(ctx, maxID, "Состояние машины изменилось после создания черновика. Обновите поездки и свяжитесь с ответственным, если проблема сохраняется.", nil)
		}
		result, err = p.Commands.IssueCreate(ctx, actor, trip.VehicleID, *draft.Context.VehicleVersion, input, key, lease)
		if err != nil {
			var commandErr *dataapi.APIError
			if errors.As(err, &commandErr) && (commandErr.Status == 404 || commandErr.Status == 409 || commandErr.Status == 422) {
				return p.sendView(ctx, maxID, "Сообщение не подтверждено. Завершённая поездка не изменена; обновите /trips и проверьте статус машины.", nil)
			}
			return err
		}
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || !vehicleIDPattern.MatchString(issue.ID) || issue.Stage != "post_return" || issue.Status != "open" || !issue.BlocksIssuance || issue.VehicleID != trip.VehicleID || issue.AuthorID != employee.ID || issue.TripID == nil || *issue.TripID != tripID || issue.InspectionID != nil || !slices.Equal(issue.AssetIDs, draft.Context.AssetIDs) {
		return errors.New("post-return issue create returned invalid aggregate")
	}
	context := draft.Context
	context.AssetIDs = append([]string(nil), draft.Context.AssetIDs...)
	context.IssueID = &issue.ID
	kind := "none"
	key, err = inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	savedResult, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: postReturnIssueFlow, Step: "done", Context: context, PendingInputKind: &kind}, key, lease)
	if err != nil {
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](savedResult)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Flow != postReturnIssueFlow || saved.Step != "done" || saved.Context.IssueID == nil || *saved.Context.IssueID != issue.ID {
		return errors.New("post-return issue completion returned invalid conversation")
	}
	return p.savedPostReturnIssue(ctx, actor, employee, maxID, &saved)
}

func (p Bootstrap) savedPostReturnIssue(ctx context.Context, actor string, employee dataapi.Employee, maxID int64, draft *dataapi.Conversation) error {
	if draft == nil || draft.Step != "done" || draft.Context.IssueID == nil || draft.Context.TripID == nil || !vehicleIDPattern.MatchString(*draft.Context.IssueID) || !vehicleIDPattern.MatchString(*draft.Context.TripID) {
		return errors.New("saved post-return issue has no ID")
	}
	issue, err := p.Data.Issue(ctx, actor, *draft.Context.IssueID)
	if err != nil {
		return err
	}
	if issue.ID != *draft.Context.IssueID || issue.AuthorID != employee.ID || issue.Stage != "post_return" || issue.Status != "open" || !issue.BlocksIssuance || issue.TripID == nil || *issue.TripID != *draft.Context.TripID || issue.InspectionID != nil || draft.Context.VehicleID == nil || issue.VehicleID != *draft.Context.VehicleID {
		return errors.New("saved post-return issue does not match conversation")
	}
	return p.sendView(ctx, maxID, "Сообщение после поездки сохранено отдельно и передано ответственному. Завершённый осмотр не изменён.", [][]maxsdk.Button{{{Text: "К поездке", Payload: "trip:" + *draft.Context.TripID}}})
}
