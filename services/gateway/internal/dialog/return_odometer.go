package dialog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
)

type ownCommandReader interface {
	OwnCommandResult(context.Context, string, string, string) (dataapi.CommandResult, error)
}

func (p Bootstrap) returnOdometer(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, input string, save bool) error {
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
