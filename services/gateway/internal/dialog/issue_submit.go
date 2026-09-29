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

func validIssueDraft(draft *dataapi.Conversation, inspectionID string) bool {
	return draft != nil && draft.Flow == "issue_before" && draft.Context.TargetID != nil && *draft.Context.TargetID == inspectionID && draft.Context.VehicleID != nil && vehicleIDPattern.MatchString(*draft.Context.VehicleID) && draft.Context.VehicleVersion != nil && *draft.Context.VehicleVersion > 0 && draft.Context.IssueCategory != nil && draft.Context.DraftText != nil && *draft.Context.DraftText != "" && len(draft.Context.AssetIDs) <= 3
}

func (p Bootstrap) savedBeforeIssue(ctx context.Context, actor string, maxID int64, draft *dataapi.Conversation) error {
	if draft == nil || draft.Context.IssueID == nil || !vehicleIDPattern.MatchString(*draft.Context.IssueID) {
		return errors.New("saved issue has no ID")
	}
	issue, err := p.Data.Issue(ctx, actor, *draft.Context.IssueID)
	if err != nil {
		return err
	}
	if issue.ID != *draft.Context.IssueID || issue.Stage != "before" || !issue.BlocksIssuance || issue.InspectionID == nil || draft.Context.TargetID == nil || *issue.InspectionID != *draft.Context.TargetID || draft.Context.VehicleID == nil || issue.VehicleID != *draft.Context.VehicleID {
		return errors.New("saved issue does not match conversation")
	}
	return p.sendView(ctx, maxID, "Замечание сохранено. Оформление отменено; машина недоступна до проверки ответственного. Можно выбрать другую машину.", [][]maxsdk.Button{{{Text: "Доступные автомобили", Payload: "cars:1"}}})
}

func (p Bootstrap) checkoutIssueSubmit(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, submit bool) error {
	draft := state.Conversation
	if draft != nil && draft.Flow == "issue_before" && draft.Step == "done" && draft.Context.TargetID != nil && *draft.Context.TargetID == inspectionID {
		return p.savedBeforeIssue(ctx, actor, maxID, draft)
	}
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || !validIssueDraft(draft, inspectionID) || draft.Version != version || draft.Step != "collect_photos" && draft.Step != "review" {
		return p.sendView(ctx, maxID, "Черновик замечания изменился или hold истёк. Обновите /menu.", nil)
	}
	if state.Checkout != nil && (state.Checkout.Status != "holding" || state.Checkout.Inspection.ID != inspectionID || state.Checkout.VehicleID != *draft.Context.VehicleID || state.Checkout.Inspection.NewDamage == nil || !*state.Checkout.Inspection.NewDamage) {
		return p.sendView(ctx, maxID, "Оформление изменилось. Обновите /menu.", nil)
	}
	if !submit {
		if _, ok := issuePhotoDraft(state); !ok {
			return p.sendView(ctx, maxID, "Осмотр изменился или hold истёк. Обновите /menu.", nil)
		}
		vehicle, err := p.Data.Vehicle(ctx, actor, *draft.Context.VehicleID)
		if err != nil {
			return err
		}
		if vehicle.Version != *draft.Context.VehicleVersion || vehicle.Status != "holding" {
			return p.sendView(ctx, maxID, "Состояние машины изменилось. Обновите /menu перед отправкой замечания.", nil)
		}
		_, label, ok := issueCategoryAlias(*draft.Context.IssueCategory)
		if !ok {
			return errors.New("issue draft has invalid category")
		}
		message := fmt.Sprintf("Замечание до поездки · %s\nКатегория: %s\nОписание: %s\nДополнительные фото: %d/3\nПосле отправки оформление отменится, машина станет недоступной до проверки.", oneLine(vehicle.Plate), label, oneLine(*draft.Context.DraftText), len(draft.Context.AssetIDs))
		rows := [][]maxsdk.Button{{{Text: "Отправить замечание", Payload: fmt.Sprintf("issue-submit:%s:%d", inspectionID, version)}}, {{Text: "Добавить фото", Payload: fmt.Sprintf("issue-photos:%s:%d", inspectionID, version)}}, {{Text: "Исправить описание", Payload: fmt.Sprintf("issue-draft:%s:%d", inspectionID, state.Checkout.Inspection.Version)}}}
		return p.sendView(ctx, maxID, message, rows)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("issue submit requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "issue.create")
	if err != nil {
		return err
	}
	input := dataapi.IssueCreateInput{Category: *draft.Context.IssueCategory, Description: *draft.Context.DraftText, InspectionID: &inspectionID, AssetIDs: append([]string{}, draft.Context.AssetIDs...)}
	lease := &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}
	result, err := p.Commands.IssueCreate(ctx, actor, *draft.Context.VehicleID, *draft.Context.VehicleVersion, input, key, lease)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404 || apiErr.Status == 422) {
			return p.sendView(ctx, maxID, "Замечание не подтверждено. Проверьте текущий статус через /menu; не начинайте поездку.", nil)
		}
		return err
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || !vehicleIDPattern.MatchString(issue.ID) || issue.Stage != "before" || issue.Status != "open" || !issue.BlocksIssuance || issue.VehicleID != *draft.Context.VehicleID || issue.AuthorID != employee.ID || issue.InspectionID == nil || *issue.InspectionID != inspectionID || !slices.Equal(issue.AssetIDs, draft.Context.AssetIDs) {
		return errors.New("issue create returned invalid aggregate")
	}
	context := draft.Context
	context.AssetIDs = append([]string(nil), draft.Context.AssetIDs...)
	context.IssueID = &issue.ID
	kind := "none"
	saveKey, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	savedResult, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: "issue_before", Step: "done", Context: context, PendingInputKind: &kind}, saveKey, lease)
	if err != nil {
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](savedResult)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Step != "done" || saved.Context.IssueID == nil || *saved.Context.IssueID != issue.ID {
		return errors.New("issue completion returned invalid conversation")
	}
	return p.savedBeforeIssue(ctx, actor, maxID, &saved)
}
