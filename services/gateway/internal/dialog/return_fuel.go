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

func returnFuelChoiceTarget(event dataapi.NormalizedEvent) (string, int64, int, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "return-fuel-set:") {
		return "", 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "return-fuel-set:"), ":")
	if len(parts) != 3 {
		return "", 0, -1, true
	}
	version, versionErr := strconv.ParseInt(parts[1], 10, 64)
	level, levelErr := strconv.Atoi(parts[2])
	if versionErr != nil || levelErr != nil || version < 1 {
		return "", 0, -1, true
	}
	return parts[0], version, level, true
}

func (p Bootstrap) returnFuel(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, inspectionID string, version int64, level int, save bool) error {
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" || state.Return.Inspection.ID != inspectionID || state.Return.Inspection.Status != "draft" {
		return p.sendView(ctx, maxID, "Осмотр после изменился. Обновите /menu.", nil)
	}
	inspection := state.Return.Inspection
	if !save {
		if inspection.Version != version {
			return p.sendView(ctx, maxID, "Кнопка топлива устарела. Обновите /menu.", nil)
		}
		rows := make([][]maxsdk.Button, 0, 5)
		for _, value := range []int{0, 25, 50, 75, 100} {
			rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("%d%%", value), Payload: fmt.Sprintf("return-fuel-set:%s:%d:%d", inspectionID, version, value)}})
		}
		return p.sendView(ctx, maxID, "Укажите уровень топлива при возврате по шкале 0–100%.", rows)
	}
	if level != 0 && level != 25 && level != 50 && level != 75 && level != 100 {
		return p.sendView(ctx, maxID, "Некорректный уровень топлива. Выберите значение через /menu.", nil)
	}
	if inspection.Version == version+1 && inspection.FuelLevel != nil && *inspection.FuelLevel == level {
		return p.sendView(ctx, maxID, fmt.Sprintf("Топливо %d%% уже сохранено. Продолжите через /menu.", level), nil)
	}
	if inspection.Version != version {
		return p.sendView(ctx, maxID, "Показания уже изменились. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return fuel requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "inspection.update")
	if err != nil {
		return err
	}
	result, err := p.Commands.InspectionUpdate(ctx, actor, inspectionID, version, dataapi.InspectionUpdateInput{FuelLevel: &level}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Осмотр после изменился. Обновите /menu.", nil)
		}
		return err
	}
	updated, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil || updated.ID != inspectionID || updated.Phase != "after" || updated.Version != version+1 || updated.FuelLevel == nil || *updated.FuelLevel != level {
		return errors.New("return fuel returned invalid inspection")
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Топливо при возврате %d%% сохранено. Продолжите через /menu.", level), nil)
}
