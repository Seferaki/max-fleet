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

func nextReturnCheckField(inspection dataapi.Inspection) string {
	switch {
	case inspection.NewDamage == nil:
		return "damage"
	case inspection.CabinClean == nil:
		return "clean"
	case inspection.ParkingAllowed == nil:
		return "parking"
	case inspection.KeysReturned == nil:
		return "keys"
	case inspection.CarLocked == nil:
		return "locked"
	default:
		return ""
	}
}

func returnCheckAnswerTarget(event dataapi.NormalizedEvent) (string, int64, string, bool, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "return-check-set:") {
		return "", 0, "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "return-check-set:"), ":")
	if len(parts) != 4 {
		return "", 0, "", false, true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 || parts[3] != "yes" && parts[3] != "no" {
		return "", 0, "", false, true
	}
	return parts[0], version, parts[2], parts[3] == "yes", true
}

func returnCheckValue(inspection dataapi.Inspection, field string) *bool {
	switch field {
	case "damage":
		return inspection.NewDamage
	case "clean":
		return inspection.CabinClean
	case "parking":
		return inspection.ParkingAllowed
	case "keys":
		return inspection.KeysReturned
	case "locked":
		return inspection.CarLocked
	default:
		return nil
	}
}

func returnCheckInput(field string, value bool) (dataapi.InspectionUpdateInput, bool) {
	switch field {
	case "damage":
		return dataapi.InspectionUpdateInput{NewDamage: &value}, true
	case "clean":
		return dataapi.InspectionUpdateInput{CabinClean: &value}, true
	case "parking":
		return dataapi.InspectionUpdateInput{ParkingAllowed: &value}, true
	case "keys":
		return dataapi.InspectionUpdateInput{KeysReturned: &value}, true
	case "locked":
		return dataapi.InspectionUpdateInput{CarLocked: &value}, true
	default:
		return dataapi.InspectionUpdateInput{}, false
	}
}

func returnCheckQuestion(field string) (string, string, string) {
	switch field {
	case "damage":
		return "Есть новые повреждения или неисправности?", "Есть", "Нет"
	case "clean":
		return "Салон и багажник чистые, мусор убран?", "Чисто", "Есть замечание"
	case "parking":
		return "Машина оставлена в допустимом месте и не мешает проезду?", "Да", "Есть проблема"
	case "keys":
		return "Ключи возвращены по правилам?", "Да", "Есть проблема"
	case "locked":
		return "Машина закрыта?", "Да", "Есть проблема"
	default:
		return "", "", ""
	}
}

func returnCheckNextRows(inspection dataapi.Inspection) [][]maxsdk.Button {
	if nextReturnCheckField(inspection) == "" {
		return [][]maxsdk.Button{{{Text: "К меню возврата", Payload: "menu"}}}
	}
	return [][]maxsdk.Button{{{Text: "Следующий вопрос", Payload: fmt.Sprintf("return-check:%s:%d", inspection.ID, inspection.Version)}}}
}

func (p Bootstrap) returnChecklist(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, field string, value bool, save bool) error {
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" || state.Return.Inspection.ID != inspectionID || state.Return.Inspection.Status != "draft" {
		return p.sendView(ctx, maxID, "Анкета возврата изменилась. Обновите /menu.", nil)
	}
	inspection := state.Return.Inspection
	if !save {
		if inspection.Version != version {
			return p.sendView(ctx, maxID, "Кнопка анкеты устарела. Обновите /menu.", nil)
		}
		field = nextReturnCheckField(inspection)
		if field == "" {
			return p.sendView(ctx, maxID, "Ответы о состоянии сохранены. Продолжите осмотр через /menu.", nil)
		}
		question, yes, no := returnCheckQuestion(field)
		rows := [][]maxsdk.Button{{{Text: yes, Payload: fmt.Sprintf("return-check-set:%s:%d:%s:yes", inspectionID, version, field)}}, {{Text: no, Payload: fmt.Sprintf("return-check-set:%s:%d:%s:no", inspectionID, version, field)}}}
		return p.sendView(ctx, maxID, question, rows)
	}
	input, valid := returnCheckInput(field, value)
	if !valid {
		return p.sendView(ctx, maxID, "Некорректный ответ анкеты. Обновите /menu.", nil)
	}
	if inspection.Version == version+1 {
		if saved := returnCheckValue(inspection, field); saved != nil && *saved == value {
			return p.sendView(ctx, maxID, "Ответ уже сохранён. Продолжите через /menu.", returnCheckNextRows(inspection))
		}
	}
	if inspection.Version != version || nextReturnCheckField(inspection) != field {
		return p.sendView(ctx, maxID, "Ответ уже изменился. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return checklist requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "inspection.update")
	if err != nil {
		return err
	}
	result, err := p.Commands.InspectionUpdate(ctx, actor, inspectionID, version, input, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Анкета возврата изменилась. Обновите /menu.", nil)
		}
		return err
	}
	updated, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil || updated.ID != inspectionID || updated.Version != version+1 || returnCheckValue(updated, field) == nil || *returnCheckValue(updated, field) != value {
		return errors.New("return checklist returned invalid inspection")
	}
	message := "Ответ сохранён."
	if field == "damage" && value || field == "clean" && !value {
		message += " Замечание нужно описать и отправить ответственному до завершения возврата."
	}
	if (field == "parking" || field == "keys" || field == "locked") && !value {
		message += " Самостоятельное завершение возврата небезопасно; сообщите ответственному и не оставляйте машину без согласования."
	}
	return p.sendView(ctx, maxID, message, returnCheckNextRows(updated))
}
