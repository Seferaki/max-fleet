package dialog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func returnIssueTarget(event dataapi.NormalizedEvent) (string, int64, string, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return "", 0, "", false
	}
	value := *event.Payload.CallbackData
	if strings.HasPrefix(value, "return-issue-kind:") {
		parts := strings.Split(strings.TrimPrefix(value, "return-issue-kind:"), ":")
		if len(parts) != 3 {
			return "", 0, "", true
		}
		version, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || version < 1 {
			return "", 0, "", true
		}
		return parts[0], version, parts[2], true
	}
	if strings.HasPrefix(value, "return-issue:") {
		id, version, _ := vehicleActionTarget(event, "return-issue:")
		return id, version, "", true
	}
	return "", 0, "", false
}

func (p Bootstrap) returnIssueDraft(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string, version int64, categoryHint, input string, save bool) error {
	if !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" || state.Return.Inspection.Status != "draft" || !save && (!vehicleIDPattern.MatchString(returnID) || version < 1 || state.Return.ID != returnID || state.Return.Version != version) {
		return p.sendView(ctx, maxID, "Черновик возврата изменился. Откройте /menu.", nil)
	}
	draftReturn := state.Return
	if !save {
		if categoryHint != "" {
			_, alias, ok := issueCategoryAlias(categoryHint)
			if !ok {
				return p.sendView(ctx, maxID, "Категория недоступна. Откройте /menu.", nil)
			}
			return p.sendView(ctx, maxID, "Опишите проблему при возврате: /issue "+alias+" <описание>. Повреждение и грязь требуют отдельных замечаний.", nil)
		}
		rows := make([][]maxsdk.Button, 0, len(issueCategories))
		for _, category := range issueCategories {
			rows = append(rows, []maxsdk.Button{{Text: category.label, Payload: fmt.Sprintf("return-issue-kind:%s:%d:%s", draftReturn.ID, draftReturn.Version, category.code)}})
		}
		return p.sendView(ctx, maxID, "Проблема при возврате. Выберите категорию и отправьте описание через /issue. Повреждение и грязь описываются отдельно. Проблемы ключей, закрытия или парковки требуют связи с ответственным: самостоятельно завершать возврат нельзя.", rows)
	}
	fields := strings.Fields(input)
	if len(fields) < 3 {
		return p.sendView(ctx, maxID, "Формат: /issue кузов <описание>. Категории: кузов, механика, чистота, ключи, другое.", nil)
	}
	category, _, ok := issueCategoryAlias(strings.ToLower(fields[1]))
	description := strings.Join(fields[2:], " ")
	if !ok || description == "" || utf8.RuneCountInString(description) > 1000 {
		return p.sendView(ctx, maxID, "Категория или описание неверны. Описание — до 1000 знаков.", nil)
	}
	if draft := state.Conversation; draft != nil && draft.Flow == "issue_after" && draft.Step == "collect_photos" && draft.Context.ReturnID != nil && *draft.Context.ReturnID == draftReturn.ID && draft.Context.IssueCategory != nil && *draft.Context.IssueCategory == category && draft.Context.DraftText != nil && *draft.Context.DraftText == description {
		return p.sendView(ctx, maxID, "Описание уже сохранено в черновике. Замечание ещё не отправлено; откройте /menu.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return issue draft requires a durable inbox lease")
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, state.Trip.VehicleID)
	if err != nil {
		return err
	}
	if vehicle.Status != "in_trip" {
		return p.sendView(ctx, maxID, "Статус машины изменился. Откройте /menu.", nil)
	}
	assetIDs := []string{}
	if draft := state.Conversation; draft != nil && draft.Flow == "issue_after" && draft.Step == "collect_photos" && draft.Context.ReturnID != nil && *draft.Context.ReturnID == draftReturn.ID {
		assetIDs = append(assetIDs, draft.Context.AssetIDs...)
	}
	kind := "photo"
	payload := dataapi.ConversationSaveInput{Flow: "issue_after", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &draftReturn.Inspection.ID, TripID: &state.Trip.ID, ReturnID: &draftReturn.ID, VehicleID: &state.Trip.VehicleID, VehicleVersion: &vehicle.Version, IssueCategory: &category, DraftText: &description, AssetIDs: assetIDs}}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, payload, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Черновик или возврат изменились. Откройте /menu.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Flow != "issue_after" || saved.Context.ReturnID == nil || *saved.Context.ReturnID != draftReturn.ID || saved.Context.DraftText == nil || *saved.Context.DraftText != description {
		return errors.New("return issue draft returned invalid conversation")
	}
	return p.sendView(ctx, maxID, "Описание сохранено в черновике возврата. Замечание ещё не отправлено; ответственный пока не уведомлён. Откройте /menu.", nil)
}
