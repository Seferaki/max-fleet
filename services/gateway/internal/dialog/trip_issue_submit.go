package dialog

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func validTripIssueDraft(draft *dataapi.Conversation, tripID string) bool {
	return draft != nil && draft.Flow == "issue_during" && draft.Context.TargetID != nil && *draft.Context.TargetID == tripID && draft.Context.TripID != nil && *draft.Context.TripID == tripID && draft.Context.VehicleID != nil && vehicleIDPattern.MatchString(*draft.Context.VehicleID) && draft.Context.VehicleVersion != nil && *draft.Context.VehicleVersion > 0 && draft.Context.IssueCategory != nil && draft.Context.DraftText != nil && *draft.Context.DraftText != "" && len(draft.Context.AssetIDs) <= 3
}

func (p Bootstrap) savedDuringIssue(ctx context.Context, actor string, maxID int64, draft *dataapi.Conversation) error {
	if draft == nil || draft.Context.IssueID == nil || !vehicleIDPattern.MatchString(*draft.Context.IssueID) {
		return errors.New("saved trip issue has no ID")
	}
	issue, err := p.Data.Issue(ctx, actor, *draft.Context.IssueID)
	if err != nil {
		return err
	}
	if issue.ID != *draft.Context.IssueID || issue.Stage != "during" || issue.Status != "open" || !issue.BlocksIssuance || issue.TripID == nil || draft.Context.TripID == nil || *issue.TripID != *draft.Context.TripID || draft.Context.VehicleID == nil || issue.VehicleID != *draft.Context.VehicleID {
		return errors.New("saved trip issue does not match conversation")
	}
	return p.sendView(ctx, maxID, "Замечание во время поездки сохранено. Машина останется недоступной для следующей выдачи до проверки ответственного. Текущая поездка продолжается.", [][]maxsdk.Button{{{Text: "Текущая поездка", Payload: "trip:" + *issue.TripID}}})
}

func (p Bootstrap) tripIssueSubmit(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, submit bool) error {
	draft := state.Conversation
	if draft != nil && draft.Flow == "issue_during" && draft.Step == "done" && draft.Context.TripID != nil && *draft.Context.TripID == tripID {
		return p.savedDuringIssue(ctx, actor, maxID, draft)
	}
	if !vehicleIDPattern.MatchString(tripID) || version < 1 || !validTripIssueDraft(draft, tripID) || draft.Version != version || draft.Step != "collect_photos" || state.Trip == nil || state.Trip.ID != tripID || state.Trip.EmployeeID != employee.ID || state.Trip.Status != "active" || state.Return != nil || state.Trip.VehicleID != *draft.Context.VehicleID {
		return p.sendView(ctx, maxID, "Черновик или поездка изменились. Откройте /menu.", nil)
	}
	if !submit {
		if _, ok := issuePhotoDraft(state); !ok {
			return p.sendView(ctx, maxID, "Черновик замечания изменился. Откройте /menu.", nil)
		}
		vehicle, err := p.Data.Vehicle(ctx, actor, *draft.Context.VehicleID)
		if err != nil {
			return err
		}
		if vehicle.Version != *draft.Context.VehicleVersion || vehicle.Status != "in_trip" {
			return p.sendView(ctx, maxID, "Состояние машины изменилось. Откройте /menu перед отправкой замечания.", nil)
		}
		_, label, ok := issueCategoryAlias(*draft.Context.IssueCategory)
		if !ok {
			return errors.New("trip issue draft has invalid category")
		}
		message := fmt.Sprintf("Проблема во время поездки · %s\nКатегория: %s\nОписание: %s\nДополнительные фото: %d/3\nПосле отправки машина останется занята текущей поездкой и будет недоступна для новой выдачи до проверки.", oneLine(vehicle.Plate), label, oneLine(*draft.Context.DraftText), len(draft.Context.AssetIDs))
		rows := [][]maxsdk.Button{{{Text: "Отправить замечание", Payload: fmt.Sprintf("trip-issue-submit:%s:%d", tripID, version)}}, {{Text: "Добавить фото", Payload: fmt.Sprintf("trip-issue-photos:%s:%d", tripID, version)}}, {{Text: "Исправить описание", Payload: fmt.Sprintf("trip-issue:%s:%d", tripID, state.Trip.Version)}}}
		return p.sendView(ctx, maxID, message, rows)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("trip issue submit requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "issue.create")
	if err != nil {
		return err
	}
	input := dataapi.IssueCreateInput{Category: *draft.Context.IssueCategory, Description: *draft.Context.DraftText, TripID: &tripID, AssetIDs: append([]string{}, draft.Context.AssetIDs...)}
	lease := &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}
	result, err := p.Commands.IssueCreate(ctx, actor, *draft.Context.VehicleID, *draft.Context.VehicleVersion, input, key, lease)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404 || apiErr.Status == 422) {
			return p.sendView(ctx, maxID, "Отправка замечания не подтверждена. Проверьте /menu; при необходимости свяжитесь с ответственным.", nil)
		}
		return err
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || !vehicleIDPattern.MatchString(issue.ID) || issue.Stage != "during" || issue.Status != "open" || !issue.BlocksIssuance || issue.VehicleID != *draft.Context.VehicleID || issue.AuthorID != employee.ID || issue.TripID == nil || *issue.TripID != tripID || !slices.Equal(issue.AssetIDs, draft.Context.AssetIDs) {
		return errors.New("trip issue create returned invalid aggregate")
	}
	context := draft.Context
	context.AssetIDs = append([]string(nil), draft.Context.AssetIDs...)
	context.IssueID = &issue.ID
	kind := "none"
	saveKey, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	savedResult, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: "issue_during", Step: "done", Context: context, PendingInputKind: &kind}, saveKey, lease)
	if err != nil {
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](savedResult)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Step != "done" || saved.Context.IssueID == nil || *saved.Context.IssueID != issue.ID {
		return errors.New("trip issue completion returned invalid conversation")
	}
	return p.savedDuringIssue(ctx, actor, maxID, &saved)
}
