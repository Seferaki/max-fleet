package dialog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func validAfterIssueDraft(draft *dataapi.Conversation, state dataapi.CurrentState) bool {
	if draft == nil || draft.Flow != "issue_after" || state.Trip == nil || state.Return == nil || draft.Context.TargetID == nil || *draft.Context.TargetID != state.Return.Inspection.ID || draft.Context.TripID == nil || *draft.Context.TripID != state.Trip.ID || draft.Context.ReturnID == nil || *draft.Context.ReturnID != state.Return.ID || draft.Context.VehicleID == nil || *draft.Context.VehicleID != state.Trip.VehicleID || draft.Context.VehicleVersion == nil || *draft.Context.VehicleVersion < 1 || draft.Context.IssueCategory == nil || draft.Context.DraftText == nil || *draft.Context.DraftText == "" || len(draft.Context.AssetIDs) > 3 {
		return false
	}
	return true
}

func missingAfterIssueReports(inspection dataapi.Inspection, issues []dataapi.Issue) []string {
	damage, dirty := false, false
	for _, issue := range issues {
		if issue.Stage != "after" || issue.InspectionID == nil || *issue.InspectionID != inspection.ID {
			continue
		}
		if issue.Category == "body_damage" || issue.Category == "mechanical" {
			damage = true
		}
		if issue.Category == "cleanliness" {
			dirty = true
		}
	}
	missing := []string{}
	if inspection.NewDamage != nil && *inspection.NewDamage && !damage {
		missing = append(missing, "повреждение или неисправность")
	}
	if inspection.CabinClean != nil && !*inspection.CabinClean && !dirty {
		missing = append(missing, "загрязнение")
	}
	return missing
}

func (p Bootstrap) savedAfterIssue(ctx context.Context, actor string, maxID int64, draft *dataapi.Conversation) error {
	if draft == nil || draft.Context.IssueID == nil || !vehicleIDPattern.MatchString(*draft.Context.IssueID) {
		return errors.New("saved return issue has no ID")
	}
	issue, err := p.Data.Issue(ctx, actor, *draft.Context.IssueID)
	if err != nil {
		return err
	}
	if issue.ID != *draft.Context.IssueID || issue.Stage != "after" || issue.Status != "open" || !issue.BlocksIssuance || issue.TripID == nil || draft.Context.TripID == nil || *issue.TripID != *draft.Context.TripID || issue.InspectionID == nil || draft.Context.TargetID == nil || *issue.InspectionID != *draft.Context.TargetID || draft.Context.VehicleID == nil || issue.VehicleID != *draft.Context.VehicleID {
		return errors.New("saved return issue does not match conversation")
	}
	state, err := p.Data.State(ctx, actor)
	if err != nil {
		return err
	}
	message := "Замечание при возврате сохранено. Машина останется недоступной для следующей выдачи до проверки ответственного."
	rows := [][]maxsdk.Button{{{Text: "К возврату", Payload: "menu"}}}
	if state.Return != nil && state.Trip != nil && state.Return.Inspection.ID == *issue.InspectionID && state.Trip.ID == *issue.TripID {
		if missing := missingAfterIssueReports(state.Return.Inspection, state.Trip.Issues); len(missing) > 0 {
			message += " Ещё нужно отдельно сообщить: " + strings.Join(missing, " и ") + "."
			rows = append([][]maxsdk.Button{{{Text: "Сообщить следующую проблему", Payload: fmt.Sprintf("return-issue:%s:%d", state.Return.ID, state.Return.Version)}}}, rows...)
		}
		inspection := state.Return.Inspection
		if inspection.ParkingAllowed != nil && !*inspection.ParkingAllowed || inspection.KeysReturned != nil && !*inspection.KeysReturned || inspection.CarLocked != nil && !*inspection.CarLocked {
			message += " Самостоятельное завершение возврата небезопасно; свяжитесь с ответственным."
		}
	}
	return p.sendView(ctx, maxID, message, rows)
}

func (p Bootstrap) returnIssueSubmit(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, submit bool) error {
	draft := state.Conversation
	if draft != nil && draft.Flow == "issue_after" && draft.Step == "done" && draft.Context.TargetID != nil && *draft.Context.TargetID == inspectionID {
		return p.savedAfterIssue(ctx, actor, maxID, draft)
	}
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" || !validAfterIssueDraft(draft, state) || state.Return.Inspection.ID != inspectionID || draft.Version != version || draft.Step != "collect_photos" {
		return p.sendView(ctx, maxID, "Черновик замечания или возврат изменились. Откройте /menu.", nil)
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
			return errors.New("return issue draft has invalid category")
		}
		message := fmt.Sprintf("Проблема при возврате · %s\nКатегория: %s\nОписание: %s\nДополнительные фото: %d/3\nПосле отправки замечание останется в истории, следующая выдача будет заблокирована до проверки.", oneLine(vehicle.Plate), label, oneLine(*draft.Context.DraftText), len(draft.Context.AssetIDs))
		rows := [][]maxsdk.Button{{{Text: "Отправить замечание", Payload: fmt.Sprintf("return-issue-submit:%s:%d", inspectionID, version)}}, {{Text: "Добавить фото", Payload: fmt.Sprintf("return-issue-photos:%s:%d", inspectionID, version)}}, {{Text: "Исправить описание", Payload: fmt.Sprintf("return-issue:%s:%d", state.Return.ID, state.Return.Version)}}}
		return p.sendView(ctx, maxID, message, rows)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return issue submit requires a durable inbox lease")
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
			return p.sendView(ctx, maxID, "Отправка замечания не подтверждена. Проверьте /menu; при небезопасной парковке, ключах или закрытии свяжитесь с ответственным.", nil)
		}
		return err
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || !vehicleIDPattern.MatchString(issue.ID) || issue.Stage != "after" || issue.Status != "open" || !issue.BlocksIssuance || issue.VehicleID != *draft.Context.VehicleID || issue.AuthorID != employee.ID || issue.TripID == nil || *issue.TripID != state.Trip.ID || issue.InspectionID == nil || *issue.InspectionID != inspectionID || !slices.Equal(issue.AssetIDs, draft.Context.AssetIDs) {
		return errors.New("return issue create returned invalid aggregate")
	}
	conversationContext := draft.Context
	conversationContext.AssetIDs = append([]string(nil), draft.Context.AssetIDs...)
	conversationContext.IssueID = &issue.ID
	kind := "none"
	saveKey, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	savedResult, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: "issue_after", Step: "done", Context: conversationContext, PendingInputKind: &kind}, saveKey, lease)
	if err != nil {
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](savedResult)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Step != "done" || saved.Context.IssueID == nil || *saved.Context.IssueID != issue.ID {
		return errors.New("return issue completion returned invalid conversation")
	}
	return p.savedAfterIssue(ctx, actor, maxID, &saved)
}
