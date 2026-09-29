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
	case inspection.KeysReturned == nil || inspection.CarLocked == nil:
		return "keys_lock"
	default:
		return ""
	}
}

func returnCheckAnswerTarget(event dataapi.NormalizedEvent) (string, int64, string, string, bool, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "return-check-set:") {
		return "", 0, "", "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "return-check-set:"), ":")
	if len(parts) != 4 {
		return "", 0, "", "", false, true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 || !validReturnCheckChoice(parts[2], parts[3]) {
		return "", 0, "", "", false, true
	}
	return parts[0], version, parts[2], parts[3], true, true
}

func validReturnCheckChoice(field, choice string) bool {
	if field == "keys_lock" {
		switch choice {
		case "both_yes", "keys_no", "lock_no", "both_no":
			return true
		}
		return false
	}
	return (field == "damage" || field == "clean" || field == "parking") && (choice == "yes" || choice == "no")
}

func returnCheckValue(inspection dataapi.Inspection, field string) *bool {
	switch field {
	case "damage":
		return inspection.NewDamage
	case "clean":
		return inspection.CabinClean
	case "parking":
		return inspection.ParkingAllowed
	default:
		return nil
	}
}

func returnCheckMatches(inspection dataapi.Inspection, field, choice string) bool {
	if !validReturnCheckChoice(field, choice) {
		return false
	}
	if field == "keys_lock" {
		if inspection.KeysReturned == nil || inspection.CarLocked == nil {
			return false
		}
		wantKeys, wantLock := false, false
		switch choice {
		case "both_yes":
			wantKeys, wantLock = true, true
		case "keys_no":
			wantLock = true
		case "lock_no":
			wantKeys = true
		}
		return *inspection.KeysReturned == wantKeys && *inspection.CarLocked == wantLock
	}
	saved := returnCheckValue(inspection, field)
	return saved != nil && *saved == (choice == "yes")
}

func returnCheckInput(field, choice string) (dataapi.InspectionUpdateInput, bool) {
	if !validReturnCheckChoice(field, choice) {
		return dataapi.InspectionUpdateInput{}, false
	}
	switch field {
	case "damage":
		value := choice == "yes"
		return dataapi.InspectionUpdateInput{NewDamage: &value}, true
	case "clean":
		value := choice == "yes"
		return dataapi.InspectionUpdateInput{CabinClean: &value}, true
	case "parking":
		value := choice == "yes"
		return dataapi.InspectionUpdateInput{ParkingAllowed: &value}, true
	case "keys_lock":
		keys, locked := false, false
		switch choice {
		case "both_yes":
			keys, locked = true, true
		case "keys_no":
			locked = true
		case "lock_no":
			keys = true
		}
		return dataapi.InspectionUpdateInput{KeysReturned: &keys, CarLocked: &locked}, true
	default:
		return dataapi.InspectionUpdateInput{}, false
	}
}

type returnCheckChoice struct {
	code  string
	label string
}

func returnCheckQuestion(field string) (string, []returnCheckChoice) {
	switch field {
	case "damage":
		return "Есть новые повреждения или неисправности?", []returnCheckChoice{{"yes", "Есть"}, {"no", "Нет"}}
	case "clean":
		return "Салон и багажник чистые, мусор убран?", []returnCheckChoice{{"yes", "Чисто"}, {"no", "Есть замечание"}}
	case "parking":
		return "Машина оставлена в допустимом месте и не мешает проезду?", []returnCheckChoice{{"yes", "Да"}, {"no", "Есть проблема"}}
	case "keys_lock":
		return "Машина закрыта, ключи возвращены по правилам?", []returnCheckChoice{
			{"both_yes", "Закрыта, ключи возвращены"},
			{"keys_no", "Закрыта, ключи не возвращены"},
			{"lock_no", "Не закрывается, ключи возвращены"},
			{"both_no", "Не закрывается, ключи не возвращены"},
		}
	default:
		return "", nil
	}
}

func returnCheckNextRows(inspection dataapi.Inspection) [][]maxsdk.Button {
	if nextReturnCheckField(inspection) == "" {
		return [][]maxsdk.Button{{{Text: "К меню возврата", Payload: "menu"}}}
	}
	return [][]maxsdk.Button{{{Text: "Следующий вопрос", Payload: fmt.Sprintf("return-check:%s:%d", inspection.ID, inspection.Version)}}}
}

func (p Bootstrap) returnChecklist(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, field, choice string, save bool) error {
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
		question, choices := returnCheckQuestion(field)
		rows := make([][]maxsdk.Button, 0, len(choices))
		for _, answer := range choices {
			rows = append(rows, []maxsdk.Button{{Text: answer.label, Payload: fmt.Sprintf("return-check-set:%s:%d:%s:%s", inspectionID, version, field, answer.code)}})
		}
		return p.sendView(ctx, maxID, question, rows)
	}
	input, valid := returnCheckInput(field, choice)
	if !valid {
		return p.sendView(ctx, maxID, "Некорректный ответ анкеты. Обновите /menu.", nil)
	}
	if inspection.Version == version+1 {
		if returnCheckMatches(inspection, field, choice) {
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
	if err != nil || updated.ID != inspectionID || updated.Version != version+1 || !returnCheckMatches(updated, field, choice) {
		return errors.New("return checklist returned invalid inspection")
	}
	message := "Ответ сохранён."
	if field == "damage" && choice == "yes" || field == "clean" && choice == "no" {
		message += " Замечание нужно описать и отправить ответственному до завершения возврата."
	}
	if field == "parking" && choice == "no" || field == "keys_lock" && choice != "both_yes" {
		message += " Самостоятельное завершение возврата небезопасно; сообщите ответственному и не оставляйте машину без согласования."
	}
	return p.sendView(ctx, maxID, message, returnCheckNextRows(updated))
}
