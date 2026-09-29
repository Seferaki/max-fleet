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

var issueCategories = []struct{ code, label, alias string }{
	{"body_damage", "Повреждение кузова", "кузов"},
	{"mechanical", "Неисправность", "механика"},
	{"cleanliness", "Загрязнение", "чистота"},
	{"keys", "Ключи", "ключи"},
	{"parking", "Проблема с парковкой", "парковка"},
	{"car_lock", "Машина не закрывается", "замок"},
	{"other", "Другое", "другое"},
}

func issueCategoryTarget(event dataapi.NormalizedEvent) (string, int64, string, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "issue-kind:") {
		return "", 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "issue-kind:"), ":")
	if len(parts) != 3 {
		return "", 0, "", true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, "", true
	}
	return parts[0], version, parts[2], true
}

func issueDraftInput(event dataapi.NormalizedEvent) (string, bool) {
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return "", false
	}
	input := strings.TrimSpace(*event.Payload.Text)
	fields := strings.Fields(input)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/issue") {
		return "", false
	}
	return input, true
}

func issueCategoryAlias(value string) (string, string, bool) {
	for _, category := range issueCategories {
		if value == category.alias || value == category.code {
			return category.code, category.alias, true
		}
	}
	return "", "", false
}

func (p Bootstrap) checkoutIssueDraft(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, categoryHint, input string, save bool) error {
	checkout := state.Checkout
	if checkout == nil || checkout.Status != "holding" || checkout.Step != "inspection" || !inspectionReadyForIssueQuestion(checkout.Inspection) || checkout.Inspection.NewDamage == nil || !*checkout.Inspection.NewDamage || !save && (!vehicleIDPattern.MatchString(inspectionID) || version < 1 || checkout.Inspection.ID != inspectionID || checkout.Inspection.Version != version) {
		return p.sendView(ctx, maxID, "Осмотр изменился или hold истёк. Обновите /menu.", nil)
	}
	if !save {
		if categoryHint != "" {
			_, alias, ok := issueCategoryAlias(categoryHint)
			if !ok {
				return p.sendView(ctx, maxID, "Категория недоступна. Откройте /menu.", nil)
			}
			return p.sendView(ctx, maxID, "Опишите замечание одной строкой: /issue "+alias+" <описание>. Дополнительные фото можно будет приложить перед отправкой.", nil)
		}
		rows := make([][]maxsdk.Button, 0, len(issueCategories))
		for _, category := range issueCategories {
			rows = append(rows, []maxsdk.Button{{Text: category.label, Payload: fmt.Sprintf("issue-kind:%s:%d:%s", checkout.Inspection.ID, checkout.Inspection.Version, category.code)}})
		}
		return p.sendView(ctx, maxID, "Выберите категорию нового замечания, затем отправьте его краткое описание.", rows)
	}
	fields := strings.Fields(input)
	if len(fields) < 3 {
		return p.sendView(ctx, maxID, "Формат: /issue кузов <описание>. Категории: кузов, механика, чистота, ключи, другое.", nil)
	}
	category, _, ok := issueCategoryAlias(strings.ToLower(fields[1]))
	description := strings.Join(fields[2:], " ")
	if !ok || description == "" || utf8.RuneCountInString(description) > 1000 {
		return p.sendView(ctx, maxID, "Категория или описание неверны. Выберите категорию через /menu; описание — до 1000 знаков.", nil)
	}
	if state.Conversation != nil && state.Conversation.Flow == "issue_before" && state.Conversation.Context.TargetID != nil && *state.Conversation.Context.TargetID == checkout.Inspection.ID && state.Conversation.Context.IssueCategory != nil && *state.Conversation.Context.IssueCategory == category && state.Conversation.Context.DraftText != nil && *state.Conversation.Context.DraftText == description {
		return p.sendView(ctx, maxID, "Описание уже сохранено в черновике. Замечание ещё не отправлено; откройте /menu.", nil)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("issue draft requires a durable inbox lease")
	}
	vehicleVersion := int64(0)
	assetIDs := []string{}
	if draft := state.Conversation; draft != nil && draft.Flow == "issue_before" && draft.Context.TargetID != nil && *draft.Context.TargetID == checkout.Inspection.ID && draft.Step != "done" {
		assetIDs = append(assetIDs, draft.Context.AssetIDs...)
		if draft.Context.VehicleVersion != nil {
			vehicleVersion = *draft.Context.VehicleVersion
		}
	}
	if vehicleVersion == 0 {
		vehicle, err := p.Data.Vehicle(ctx, actor, checkout.VehicleID)
		if err != nil {
			return err
		}
		vehicleVersion = vehicle.Version
	}
	kind := "photo"
	payload := dataapi.ConversationSaveInput{Flow: "issue_before", Step: "collect_photos", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &checkout.Inspection.ID, VehicleID: &checkout.VehicleID, VehicleVersion: &vehicleVersion, IssueCategory: &category, DraftText: &description, AssetIDs: assetIDs}}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, payload, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Черновик изменился или hold истёк. Обновите /menu.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Version != state.ConversationVersion+1 || saved.Flow != "issue_before" || saved.Context.DraftText == nil || *saved.Context.DraftText != description || saved.Context.IssueCategory == nil || *saved.Context.IssueCategory != category {
		return errors.New("issue draft returned invalid conversation")
	}
	return p.sendView(ctx, maxID, "Описание сохранено в черновике. Замечание ещё не отправлено; поездка не начнётся. Откройте /menu.", nil)
}
