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

const returnOdometerConfirmationThresholdKM int64 = 1000

func returnOdometerConfirmTarget(event dataapi.NormalizedEvent) (string, int64, int64, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return "", 0, 0, false
	}
	payload := *event.Payload.CallbackData
	const prefix = "return-odometer-confirm:"
	if !strings.HasPrefix(payload, prefix) {
		return "", 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(payload, prefix), ":")
	if len(parts) != 3 || !vehicleIDPattern.MatchString(parts[0]) {
		return "", 0, 0, true
	}
	version, versionErr := strconv.ParseInt(parts[1], 10, 64)
	value, valueErr := strconv.ParseInt(parts[2], 10, 64)
	if versionErr != nil || version < 1 || valueErr != nil || value < 0 {
		return "", 0, 0, true
	}
	return parts[0], version, value, true
}

func returnOdometerNeedsConfirmation(value, baseline int64) bool {
	return value > baseline && value-baseline > returnOdometerConfirmationThresholdKM
}

func returnOdometerConfirmationRows(inspectionID string, version, value int64) [][]maxsdk.Button {
	return [][]maxsdk.Button{
		{{Text: "Подтвердить пробег", Payload: fmt.Sprintf("return-odometer-confirm:%s:%d:%d", inspectionID, version, value)}},
		{{Text: "Изменить пробег", Payload: fmt.Sprintf("return-odometer:%s:%d", inspectionID, version)}},
	}
}

func (p Bootstrap) confirmReturnOdometer(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version, value int64) error {
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || value < 0 || !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" || state.Return.Inspection.Status != "draft" || state.Return.Inspection.ID != inspectionID {
		return p.sendView(ctx, maxID, "Подтверждение пробега устарело. Обновите /menu.", nil)
	}
	inspection := state.Return.Inspection
	if inspection.Version == version+1 && inspection.OdometerKM != nil && *inspection.OdometerKM == value {
		return p.sendView(ctx, maxID, fmt.Sprintf("Пробег %d км уже сохранён. Продолжите через /menu.", value), nil)
	}
	if inspection.Version != version {
		return p.sendView(ctx, maxID, "Осмотр изменился после запроса подтверждения. Проверьте данные через /menu.", nil)
	}
	baseline := int64(0)
	if state.Trip.BeforeInspection.OdometerKM != nil {
		baseline = *state.Trip.BeforeInspection.OdometerKM
	}
	if baseline < 0 || value < baseline || !returnOdometerNeedsConfirmation(value, baseline) {
		return p.sendView(ctx, maxID, "Подтверждение не соответствует текущему пробегу. Введите /odometer N ещё раз.", nil)
	}
	return p.returnOdometer(ctx, item, actor, maxID, employee, state, inspectionID, version, fmt.Sprintf("/odometer %d", value), true, true)
}

type ownCommandReader interface {
	OwnCommandResult(context.Context, string, string, string) (dataapi.CommandResult, error)
}

func (p Bootstrap) returnOdometer(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, input string, save, confirmed bool) error {
	if !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" || state.Return.Inspection.Status != "draft" || state.Return.Inspection.ID == "" {
		return p.sendView(ctx, maxID, "Осмотр после изменился. Обновите /menu.", nil)
	}
	inspection := state.Return.Inspection
	if !save {
		if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || inspection.ID != inspectionID || inspection.Version != version {
			return p.sendView(ctx, maxID, "Кнопка пробега устарела. Обновите /menu.", nil)
		}
		baseline := int64(0)
		if state.Trip.BeforeInspection.OdometerKM != nil {
			baseline = *state.Trip.BeforeInspection.OdometerKM
		}
		if baseline < 0 {
			return errors.New("return odometer baseline is invalid")
		}
		return p.sendView(ctx, maxID, fmt.Sprintf("Укажите пробег при возврате целым числом: /odometer N. Он не может быть меньше %d км при выезде.", baseline), nil)
	}
	fields := strings.Fields(input)
	if len(fields) != 2 || fields[1] == "" || strings.Trim(fields[1], "0123456789") != "" {
		return p.sendView(ctx, maxID, "Укажите неотрицательное целое число километров: /odometer 12000.", nil)
	}
	value, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return p.sendView(ctx, maxID, "Пробег вне допустимого диапазона. Укажите целое число километров.", nil)
	}
	if state.Trip.BeforeInspection.OdometerKM != nil && value < *state.Trip.BeforeInspection.OdometerKM {
		return p.sendView(ctx, maxID, "Пробег меньше показания при выезде. Проверьте число и отправьте /odometer N ещё раз.", nil)
	}
	baseline := int64(0)
	if state.Trip.BeforeInspection.OdometerKM != nil {
		baseline = *state.Trip.BeforeInspection.OdometerKM
	}
	if baseline < 0 {
		return errors.New("return odometer baseline is invalid")
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return odometer requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "inspection.update")
	if err != nil {
		return err
	}
	reader, ok := p.Data.(ownCommandReader)
	if !ok {
		return errors.New("command result reader is not configured")
	}
	previous, err := reader.OwnCommandResult(ctx, actor, key, "inspection.update")
	if err == nil {
		saved, decodeErr := dataapi.DecodeAggregate[dataapi.Inspection](previous)
		if decodeErr != nil || saved.ID != inspection.ID || saved.OdometerKM == nil || *saved.OdometerKM != value || saved.Phase != "after" {
			return errors.New("return odometer prior result does not match input")
		}
		return p.sendView(ctx, maxID, fmt.Sprintf("Пробег %d км уже сохранён. Продолжите через /menu.", value), nil)
	}
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		return err
	}
	if !confirmed && returnOdometerNeedsConfirmation(value, baseline) {
		delta := value - baseline
		message := fmt.Sprintf("Проверьте ввод: пробег увеличится с %d до %d км, прирост составит %d км. Подтвердите значение или введите пробег заново.", baseline, value, delta)
		return p.sendView(ctx, maxID, message, returnOdometerConfirmationRows(inspection.ID, inspection.Version, value))
	}
	result, err := p.Commands.InspectionUpdate(ctx, actor, inspection.ID, inspection.Version, dataapi.InspectionUpdateInput{OdometerKM: &value}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		if errors.As(err, &apiErr) && apiErr.Status == 422 && apiErr.Code == "ODOMETER_ROLLBACK" {
			return p.sendView(ctx, maxID, "Пробег меньше последнего подтверждённого значения. Проверьте число и отправьте /odometer N ещё раз.", nil)
		}
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Осмотр после изменился. Обновите /menu.", nil)
		}
		return err
	}
	updated, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil || updated.ID != inspection.ID || updated.Phase != "after" || updated.Version != inspection.Version+1 || updated.OdometerKM == nil || *updated.OdometerKM != value {
		return errors.New("return odometer returned invalid inspection")
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Пробег при возврате %d км сохранён. Продолжите через /menu.", value), nil)
}
