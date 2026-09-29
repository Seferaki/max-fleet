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

const adminOdometerCorrectionFlow = "vehicle_odometer_correction"

type adminOdometerRequest struct {
	recognized bool
	kind       string
	page       int
	vehicleID  string
	version    int64
	valueText  string
}

type adminOdometerCorrector interface {
	VehicleCorrectSnapshot(context.Context, string, string, int64, dataapi.VehicleSnapshotCorrectionInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
}

func parseAdminOdometerEvent(event dataapi.NormalizedEvent) adminOdometerRequest {
	if event.EventType == "message_created" && event.Payload.Kind == "text" && event.Payload.Text != nil {
		value := strings.TrimSpace(*event.Payload.Text)
		lower := strings.ToLower(value)
		if lower == "/adminodo" {
			return adminOdometerRequest{recognized: true, kind: "list", page: 1}
		}
		if strings.HasPrefix(lower, "/adminodo ") {
			return adminOdometerRequest{recognized: true, kind: "input", valueText: strings.TrimSpace(value[len("/adminodo "):])}
		}
		return adminOdometerRequest{}
	}
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return adminOdometerRequest{}
	}
	parts := strings.Split(*event.Payload.CallbackData, ":")
	if len(parts) == 0 || parts[0] != "admin-odo" {
		return adminOdometerRequest{}
	}
	request := adminOdometerRequest{recognized: true}
	switch {
	case len(parts) == 3 && parts[1] == "list":
		request.kind, request.page = "list", parsePositiveInt(parts[2])
	case len(parts) == 4 && (parts[1] == "select" || parts[1] == "resume" || parts[1] == "confirm" || parts[1] == "cancel"):
		request.kind = parts[1]
		request.vehicleID = parts[2]
		request.version = parseAdminIssueVersion(parts[3])
	default:
		request.kind = "invalid"
	}
	return request
}

func (p Bootstrap) handleAdminOdometerRequest(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, request adminOdometerRequest) error {
	if employee.Role != "admin" {
		return p.sendView(ctx, maxID, "Коррекция пробега доступна только администратору автопарка.", nil)
	}
	switch request.kind {
	case "list":
		if state.Conversation != nil && state.Conversation.Step != "done" && state.Conversation.Step != "cancelled" {
			if state.Conversation.Flow == adminOdometerCorrectionFlow {
				return p.resumeAdminOdometerCorrection(ctx, item, actor, maxID, employee, state,
					valueOrEmpty(state.Conversation.Context.VehicleID), valueOrZero(state.Conversation.Context.VehicleVersion))
			}
			return p.sendView(ctx, maxID, "Сначала продолжите или завершите сохранённый диалог через /menu. Текущий черновик не изменён.", nil)
		}
		return p.showAdminOdometerVehicles(ctx, actor, maxID, request.page)
	case "select":
		return p.beginAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, request.vehicleID, request.version)
	case "input":
		return p.saveAdminOdometerValue(ctx, item, actor, maxID, employee, state, request.valueText)
	case "resume":
		return p.resumeAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, request.vehicleID, request.version)
	case "confirm":
		return p.confirmAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, request.vehicleID, request.version)
	case "cancel":
		return p.cancelAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, request.vehicleID, request.version)
	default:
		return p.sendView(ctx, maxID, "Кнопка коррекции пробега повреждена или устарела. Откройте /adminodo.", nil)
	}
}

func (p Bootstrap) showAdminOdometerVehicles(ctx context.Context, actor string, maxID int64, wanted int) error {
	if wanted < 1 || wanted > 20 {
		return p.sendView(ctx, maxID, "Страница списка машин недоступна. Откройте /adminodo.", nil)
	}
	available := false
	cursor := ""
	active := make([]dataapi.Vehicle, 0, wanted*5+1)
	moreRawPages := false
	for pageNo := 0; pageNo < 20; pageNo++ {
		page, err := p.Data.Vehicles(ctx, actor, dataapi.VehicleFilter{Available: &available, Limit: 50, Cursor: cursor})
		if err != nil {
			return err
		}
		for _, vehicle := range page.Items {
			if vehicle.Status == "holding" || vehicle.Status == "in_trip" {
				active = append(active, vehicle)
				if len(active) > wanted*5 {
					break
				}
			}
		}
		if len(active) > wanted*5 || page.NextCursor == nil {
			moreRawPages = page.NextCursor != nil
			break
		}
		cursor = *page.NextCursor
		moreRawPages = true
	}
	start := (wanted - 1) * 5
	if start >= len(active) {
		if moreRawPages && wanted == 20 {
			return p.sendView(ctx, maxID, "Список превышает предел безопасной навигации. Уточните машину с ответственным за автопарк.", nil)
		}
		return p.sendView(ctx, maxID, "Сейчас нет активных удержаний или поездок для коррекции.", [][]maxsdk.Button{{{Text: "Обновить список", Payload: "admin-odo:list:1"}}})
	}
	end := start + 5
	if end > len(active) {
		end = len(active)
	}
	rows := make([][]maxsdk.Button, 0, 7)
	lines := []string{fmt.Sprintf("Активные машины · страница %d", wanted)}
	for _, vehicle := range active[start:end] {
		if !vehicleIDPattern.MatchString(vehicle.ID) || vehicle.Version < 1 {
			return errors.New("data-api: invalid active vehicle projection")
		}
		lines = append(lines, fmt.Sprintf("%s · %s · пробег %s км", shortLabel(vehicle.Plate), adminOdometerStatusLabel(vehicle.Status), formatOdometer(vehicle.CurrentOdometerKM)))
		rows = append(rows, []maxsdk.Button{{Text: shortLabel(vehicle.Plate + " · " + adminOdometerStatusLabel(vehicle.Status)), Payload: fmt.Sprintf("admin-odo:select:%s:%d", vehicle.ID, vehicle.Version)}})
	}
	controls := []maxsdk.Button{{Text: "Обновить", Payload: fmt.Sprintf("admin-odo:list:%d", wanted)}}
	if wanted > 1 {
		controls = append(controls, maxsdk.Button{Text: "Назад", Payload: fmt.Sprintf("admin-odo:list:%d", wanted-1)})
	}
	if len(active) > end || moreRawPages {
		controls = append(controls, maxsdk.Button{Text: "Далее", Payload: fmt.Sprintf("admin-odo:list:%d", wanted+1)})
	}
	rows = append(rows, controls)
	return p.sendView(ctx, maxID, strings.Join(lines, "\n"), rows)
}

func (p Bootstrap) beginAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64) error {
	if !vehicleIDPattern.MatchString(vehicleID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка машины повреждена. Откройте /adminodo.", nil)
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
	if err != nil {
		return p.adminOdometerReadError(ctx, maxID, vehicleID, err)
	}
	if vehicle.Version != version || !activeOdometerVehicle(vehicle) {
		return p.showAdminOdometerVehicles(ctx, actor, maxID, 1)
	}
	if conversation := state.Conversation; conversation != nil {
		if conversation.Flow == adminOdometerCorrectionFlow && conversation.Context.VehicleID != nil && *conversation.Context.VehicleID == vehicleID &&
			conversation.Context.VehicleVersion != nil && *conversation.Context.VehicleVersion == version {
			return p.renderAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, conversation)
		}
		if conversation.Step != "done" && conversation.Step != "cancelled" {
			return p.sendView(ctx, maxID, "Сначала продолжите или завершите сохранённый диалог через /menu. Новый черновик не создан.", nil)
		}
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin odometer conversation requires a durable inbox lease")
	}
	pending := "text"
	input := dataapi.ConversationSaveInput{Flow: adminOdometerCorrectionFlow, Step: "await_value", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{VehicleID: &vehicleID, VehicleVersion: &vehicle.Version}}
	saved, err := p.saveAdminOdometerConversation(ctx, item, actor, employee, state, input)
	if err != nil {
		return p.adminOdometerConversationError(ctx, maxID, err)
	}
	return p.renderAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, &saved)
}

func (p Bootstrap) saveAdminOdometerValue(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, valueText string) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminOdometerCorrectionFlow || conversation.Context.VehicleID == nil || conversation.Context.VehicleVersion == nil {
		return p.sendView(ctx, maxID, "Сначала выберите активную машину через /adminodo.", nil)
	}
	if conversation.Step == "confirm" && conversation.Context.CorrectionOdometerKM != nil && conversation.Context.DraftText != nil {
		value, reason, valid := parseAdminOdometerValue(valueText)
		if valid && value == *conversation.Context.CorrectionOdometerKM && reason == *conversation.Context.DraftText {
			return p.renderAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, conversation)
		}
		return p.sendView(ctx, maxID, "Значение и причина уже сохранены. Подтвердите или отмените исправление перед новым вводом.", [][]maxsdk.Button{{{Text: "Продолжить", Payload: fmt.Sprintf("admin-odo:resume:%s:%d", *conversation.Context.VehicleID, *conversation.Context.VehicleVersion)}}, {{Text: "Отменить", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", *conversation.Context.VehicleID, *conversation.Context.VehicleVersion)}}})
	}
	if conversation.Step != "await_value" {
		return p.sendView(ctx, maxID, "Шаг коррекции пробега устарел. Откройте /adminodo.", nil)
	}
	value, reason, valid := parseAdminOdometerValue(valueText)
	if !valid {
		return p.sendView(ctx, maxID, "Формат неверен. Отправьте: /adminodo 42150 | причина исправления", nil)
	}
	vehicleID, expectedVersion := *conversation.Context.VehicleID, *conversation.Context.VehicleVersion
	vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
	if err != nil {
		return p.adminOdometerReadError(ctx, maxID, vehicleID, err)
	}
	if vehicle.Version != expectedVersion || !activeOdometerVehicle(vehicle) {
		return p.cancelStaleAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, vehicleID, vehicle.Version)
	}
	if vehicle.CurrentOdometerKM != nil && *vehicle.CurrentOdometerKM == value {
		return p.sendView(ctx, maxID, "Показание совпадает с текущим. Машина не изменена. Введите другое значение или отмените коррекцию.", [][]maxsdk.Button{{{Text: "Отменить", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, expectedVersion)}}})
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin odometer input requires a durable inbox lease")
	}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminOdometerCorrectionFlow, Step: "confirm", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{VehicleID: &vehicleID, VehicleVersion: &expectedVersion, CorrectionOdometerKM: &value, DraftText: &reason}}
	saved, err := p.saveAdminOdometerConversation(ctx, item, actor, employee, state, input)
	if err != nil {
		return p.adminOdometerConversationError(ctx, maxID, err)
	}
	return p.renderAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, &saved)
}

func parseAdminOdometerValue(value string) (int64, string, bool) {
	valueText, reason, separated := strings.Cut(value, "|")
	if !separated {
		return 0, "", false
	}
	valueText, reason = strings.TrimSpace(valueText), strings.TrimSpace(reason)
	valueKM, err := strconv.ParseInt(valueText, 10, 64)
	if err != nil || valueKM < 0 || utf8.RuneCountInString(reason) == 0 || utf8.RuneCountInString(reason) > 1000 {
		return 0, "", false
	}
	return valueKM, reason, true
}

func (p Bootstrap) renderAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, conversation *dataapi.Conversation) error {
	if conversation == nil || conversation.Flow != adminOdometerCorrectionFlow || conversation.Context.VehicleID == nil || conversation.Context.VehicleVersion == nil {
		return p.sendView(ctx, maxID, "Сохранённый диалог коррекции неполон. Откройте /adminodo.", nil)
	}
	vehicleID, version := *conversation.Context.VehicleID, *conversation.Context.VehicleVersion
	if !vehicleIDPattern.MatchString(vehicleID) || version < 1 {
		return p.sendView(ctx, maxID, "Сохранённая ссылка на машину повреждена. Откройте /adminodo.", nil)
	}
	if conversation.Step == "done" || conversation.Step == "cancelled" {
		return p.sendView(ctx, maxID, "Коррекция уже завершена или отменена. Откройте /adminodo для нового действия.", nil)
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
	if err != nil {
		return p.adminOdometerReadError(ctx, maxID, vehicleID, err)
	}
	if vehicle.Version != version || !activeOdometerVehicle(vehicle) {
		return p.cancelStaleAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, vehicleID, vehicle.Version)
	}
	switch conversation.Step {
	case "await_value":
		return p.sendView(ctx, maxID, fmt.Sprintf("Коррекция пробега · %s\nСтатус: %s\nТекущее показание: %s км\nВерсия машины: %d\n\nОтправьте: /adminodo <км> | причина исправления\nЗначение и причина сохранятся до отдельного подтверждения.", vehicle.Plate, adminOdometerStatusLabel(vehicle.Status), formatOdometer(vehicle.CurrentOdometerKM), vehicle.Version),
			[][]maxsdk.Button{{{Text: "Отменить коррекцию", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, version)}}})
	case "confirm":
		if conversation.Context.CorrectionOdometerKM == nil || conversation.Context.DraftText == nil || strings.TrimSpace(*conversation.Context.DraftText) == "" || conversation.PendingInputKind == nil || *conversation.PendingInputKind != "none" {
			return p.sendView(ctx, maxID, "Сохранённое подтверждение неполно. Отмените его и начните заново через /adminodo.", [][]maxsdk.Button{{{Text: "Отменить коррекцию", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, version)}}})
		}
		oldValue := formatOdometer(vehicle.CurrentOdometerKM)
		return p.sendView(ctx, maxID, fmt.Sprintf("Подтвердите исправление пробега\nМашина: %s · %s\nБыло: %s км\nСтанет: %s км\nПричина: %s\n\nБудет изменён только одометр snapshot. Бронь/поездка, исходный осмотр и фотографии сохранятся.", vehicle.Plate, adminOdometerStatusLabel(vehicle.Status), oldValue, formatOdometer(conversation.Context.CorrectionOdometerKM), *conversation.Context.DraftText),
			[][]maxsdk.Button{{{Text: "Подтвердить исправление", Payload: fmt.Sprintf("admin-odo:confirm:%s:%d", vehicleID, version)}}, {{Text: "Отменить", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, version)}}})
	default:
		return p.sendView(ctx, maxID, "Шаг коррекции пробега неизвестен. Откройте /adminodo.", nil)
	}
}

func (p Bootstrap) confirmAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64) error {
	conversation := state.Conversation
	if !vehicleIDPattern.MatchString(vehicleID) || version < 1 || conversation == nil || conversation.Flow != adminOdometerCorrectionFlow ||
		conversation.Context.VehicleID == nil || conversation.Context.VehicleVersion == nil || *conversation.Context.VehicleID != vehicleID {
		return p.sendView(ctx, maxID, "Подтверждение устарело. Откройте /adminodo и проверьте машину заново.", nil)
	}
	if conversation.Step == "done" {
		if *conversation.Context.VehicleVersion != version+1 {
			return p.sendView(ctx, maxID, "Подтверждение устарело. Откройте /adminodo и проверьте машину заново.", nil)
		}
		vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
		if err != nil {
			return p.adminOdometerReadError(ctx, maxID, vehicleID, err)
		}
		return p.sendView(ctx, maxID, fmt.Sprintf("Коррекция уже сохранена. Текущий пробег: %s км.", formatOdometer(vehicle.CurrentOdometerKM)), nil)
	}
	if *conversation.Context.VehicleVersion != version {
		return p.sendView(ctx, maxID, "Подтверждение устарело. Откройте /adminodo и проверьте машину заново.", nil)
	}
	if conversation.Step != "confirm" || conversation.Context.CorrectionOdometerKM == nil || conversation.Context.DraftText == nil ||
		strings.TrimSpace(*conversation.Context.DraftText) == "" || conversation.PendingInputKind == nil || *conversation.PendingInputKind != "none" {
		return p.renderAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, conversation)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin odometer confirmation requires a durable inbox lease")
	}
	corrector, ok := p.Commands.(adminOdometerCorrector)
	if !ok {
		return errors.New("dialog vehicle snapshot corrector is not configured")
	}
	key, err := inboxworker.CommandKey(item, "vehicle.correct_snapshot")
	if err != nil {
		return err
	}
	result, recovered, err := p.recoverAdminOdometerCommand(ctx, actor, key, vehicleID, version, *conversation.Context.CorrectionOdometerKM)
	if err != nil {
		return err
	}
	if !recovered {
		vehicle, readErr := p.Data.Vehicle(ctx, actor, vehicleID)
		if readErr != nil {
			return p.adminOdometerReadError(ctx, maxID, vehicleID, readErr)
		}
		if vehicle.Version != version || !activeOdometerVehicle(vehicle) || vehicle.CurrentOdometerKM != nil && *vehicle.CurrentOdometerKM == *conversation.Context.CorrectionOdometerKM {
			return p.cancelStaleAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, vehicleID, vehicle.Version)
		}
		result, err = corrector.VehicleCorrectSnapshot(ctx, actor, vehicleID, version,
			dataapi.VehicleSnapshotCorrectionInput{Reason: *conversation.Context.DraftText, OdometerKM: conversation.Context.CorrectionOdometerKM, Confirmation: true},
			key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
		if err != nil {
			return p.adminOdometerCommandError(ctx, item, actor, maxID, employee, state, vehicleID, version, err)
		}
	}
	corrected, err := dataapi.DecodeAggregate[dataapi.Vehicle](result)
	if err != nil || corrected.ID != vehicleID || corrected.Version != version+1 || !activeOdometerVehicle(corrected) ||
		corrected.CurrentOdometerKM == nil || *corrected.CurrentOdometerKM != *conversation.Context.CorrectionOdometerKM {
		return errors.New("vehicle.correct_snapshot returned an invalid odometer correction")
	}
	completedVersion, pending := corrected.Version, "none"
	completed := dataapi.ConversationSaveInput{Flow: adminOdometerCorrectionFlow, Step: "done", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{VehicleID: &vehicleID, VehicleVersion: &completedVersion}}
	if _, err := p.saveAdminOdometerConversation(ctx, item, actor, employee, state, completed); err != nil {
		return p.adminOdometerConversationError(ctx, maxID, err)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Пробег исправлен: %s км. Причина сохранена в аудите.", formatOdometer(corrected.CurrentOdometerKM)),
		[][]maxsdk.Button{{{Text: "Активные машины", Payload: "admin-odo:list:1"}}})
}

func (p Bootstrap) adminOdometerCommandError(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64, err error) error {
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case 400, 422:
		return p.sendView(ctx, maxID, "API не принял исправление. Машина не изменена; проверьте данные, затем повторно подтвердите или отмените сохранённый черновик.", [][]maxsdk.Button{
			{{Text: "Повторно проверить", Payload: fmt.Sprintf("admin-odo:resume:%s:%d", vehicleID, version)},
				{Text: "Отменить", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, version)}},
		})
	case 403:
		return p.sendView(ctx, maxID, "Право администратора изменилось. Коррекция не применена; проверьте доступ перед новой попыткой.", nil)
	case 404:
		return p.sendView(ctx, maxID, "Машина больше недоступна. Коррекция не применена; обновите список через /adminodo.", [][]maxsdk.Button{{
			{Text: "Отменить черновик", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, version)},
		}})
	case 409:
		current, currentErr := p.Data.Vehicle(ctx, actor, vehicleID)
		if currentErr != nil {
			var readAPIError *dataapi.APIError
			if errors.As(currentErr, &readAPIError) && readAPIError.Status == 404 {
				return p.sendView(ctx, maxID, "Машина больше недоступна. Коррекция не применена; отмените сохранённый черновик.", [][]maxsdk.Button{{
					{Text: "Отменить черновик", Payload: fmt.Sprintf("admin-odo:cancel:%s:%d", vehicleID, version)},
				}})
			}
			return currentErr
		}
		return p.cancelStaleAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, vehicleID, current.Version)
	default:
		return err
	}
}

func (p Bootstrap) recoverAdminOdometerCommand(ctx context.Context, actor, key, vehicleID string, version, value int64) (dataapi.CommandResult, bool, error) {
	reader, ok := p.Data.(ownCommandReader)
	if !ok {
		return dataapi.CommandResult{}, false, errors.New("command result reader is not configured")
	}
	result, err := reader.OwnCommandResult(ctx, actor, key, "vehicle.correct_snapshot")
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return dataapi.CommandResult{}, false, nil
		}
		return dataapi.CommandResult{}, false, err
	}
	vehicle, err := dataapi.DecodeAggregate[dataapi.Vehicle](result)
	if err != nil || vehicle.ID != vehicleID || vehicle.Version != version+1 || !activeOdometerVehicle(vehicle) || vehicle.CurrentOdometerKM == nil || *vehicle.CurrentOdometerKM != value {
		return dataapi.CommandResult{}, false, errors.New("saved vehicle.correct_snapshot result does not match the requested action")
	}
	return result, true, nil
}

func (p Bootstrap) cancelAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64) error {
	conversation := state.Conversation
	if !vehicleIDPattern.MatchString(vehicleID) || version < 1 || conversation == nil || conversation.Flow != adminOdometerCorrectionFlow ||
		conversation.Context.VehicleID == nil || conversation.Context.VehicleVersion == nil || *conversation.Context.VehicleID != vehicleID || *conversation.Context.VehicleVersion != version {
		return p.sendView(ctx, maxID, "Диалог коррекции уже изменился. Откройте /adminodo.", nil)
	}
	if conversation.Step == "done" || conversation.Step == "cancelled" {
		return p.sendView(ctx, maxID, "Коррекция уже завершена или отменена.", nil)
	}
	return p.finishAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, vehicleID, version, "cancelled", "Коррекция отменена. Одометр и данные поездки не менялись.")
}

func (p Bootstrap) cancelStaleAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, currentVersion int64) error {
	if conversation := state.Conversation; conversation != nil && conversation.Flow == adminOdometerCorrectionFlow &&
		conversation.Context.VehicleID != nil && *conversation.Context.VehicleID == vehicleID && conversation.Context.VehicleVersion != nil {
		return p.finishAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, vehicleID, currentVersion, "cancelled",
			"Машина изменилась. Старое подтверждение отменено; текущие данные не затронуты.")
	}
	return p.sendView(ctx, maxID, "Машина изменилась. Откройте /adminodo и начните заново.", nil)
}

func (p Bootstrap) finishAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, vehicleVersion int64, step, message string) error {
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin odometer conversation update requires a durable inbox lease")
	}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminOdometerCorrectionFlow, Step: step, PendingInputKind: &pending,
		Context: dataapi.ConversationContext{VehicleID: &vehicleID, VehicleVersion: &vehicleVersion}}
	if _, err := p.saveAdminOdometerConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminOdometerConversationError(ctx, maxID, err)
	}
	return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Активные машины", Payload: "admin-odo:list:1"}}})
}

func (p Bootstrap) resumeAdminOdometerCorrection(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminOdometerCorrectionFlow || conversation.Context.VehicleID == nil || conversation.Context.VehicleVersion == nil ||
		!vehicleIDPattern.MatchString(vehicleID) || version < 1 || *conversation.Context.VehicleID != vehicleID || *conversation.Context.VehicleVersion != version {
		return p.sendView(ctx, maxID, "Сохранённая коррекция уже изменилась. Откройте /adminodo.", nil)
	}
	if conversation.Step == "done" || conversation.Step == "cancelled" {
		return p.sendView(ctx, maxID, "Коррекция уже завершена или отменена. Откройте /adminodo.", nil)
	}
	return p.renderAdminOdometerCorrection(ctx, item, actor, maxID, employee, state, conversation)
}

func (p Bootstrap) saveAdminOdometerConversation(ctx context.Context, item dataapi.InboxClaimItem, actor string, employee dataapi.Employee, state dataapi.CurrentState, input dataapi.ConversationSaveInput) (dataapi.Conversation, error) {
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" || input.Context.VehicleID == nil || input.Context.VehicleVersion == nil {
		return dataapi.Conversation{}, errors.New("admin odometer conversation save requires a durable inbox lease and vehicle")
	}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return dataapi.Conversation{}, err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, input, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		return dataapi.Conversation{}, err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Flow != adminOdometerCorrectionFlow || saved.Step != input.Step || saved.Version != state.ConversationVersion+1 ||
		saved.Context.VehicleID == nil || *saved.Context.VehicleID != *input.Context.VehicleID || saved.Context.VehicleVersion == nil || *saved.Context.VehicleVersion != *input.Context.VehicleVersion ||
		!sameOptionalInt64(saved.Context.CorrectionOdometerKM, input.Context.CorrectionOdometerKM) || !sameOptionalString(saved.Context.DraftText, input.Context.DraftText) {
		return dataapi.Conversation{}, errors.New("conversation.save returned an invalid admin odometer state")
	}
	return saved, nil
}

func sameOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameOptionalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (p Bootstrap) adminOdometerConversationError(ctx context.Context, maxID int64, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
		return p.sendView(ctx, maxID, "Машина или сохранённый диалог изменились. Откройте /adminodo и проверьте текущие данные.", nil)
	}
	return err
}

func (p Bootstrap) adminOdometerReadError(ctx context.Context, maxID int64, vehicleID string, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return p.sendView(ctx, maxID, "Машина недоступна. Откройте /adminodo и обновите список.", nil)
	}
	return err
}

func activeOdometerVehicle(vehicle dataapi.Vehicle) bool {
	return vehicle.Status == "holding" || vehicle.Status == "in_trip"
}

func adminOdometerStatusLabel(status string) string {
	if status == "holding" {
		return "в удержании"
	}
	if status == "in_trip" {
		return "в поездке"
	}
	return "недоступна"
}

func formatOdometer(value *int64) string {
	if value == nil {
		return "не указан"
	}
	return strconv.FormatInt(*value, 10)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
