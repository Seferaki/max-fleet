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

func fuelChoiceTarget(event dataapi.NormalizedEvent) (string, int64, int, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "fuel-set:") {
		return "", 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "fuel-set:"), ":")
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

func (p Bootstrap) checkoutFuel(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, inspectionID string, version int64, level int, save bool) error {
	checkout := state.Checkout
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || checkout == nil || checkout.Status != "holding" || checkout.Step != "inspection" || checkout.Inspection.ID != inspectionID || checkout.Inspection.Version != version || checkout.Inspection.Phase != "before" || checkout.Inspection.Status != "draft" {
		return p.sendView(ctx, maxID, "Шаг осмотра изменился или hold истёк. Обновите /menu.", nil)
	}
	if !save {
		rows := make([][]maxsdk.Button, 0, 5)
		for _, value := range []int{0, 25, 50, 75, 100} {
			rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("%d%%", value), Payload: fmt.Sprintf("fuel-set:%s:%d:%d", inspectionID, version, value)}})
		}
		return p.sendView(ctx, maxID, "Укажите уровень топлива по шкале 0–100%.", rows)
	}
	if level != 0 && level != 25 && level != 50 && level != 75 && level != 100 {
		return p.sendView(ctx, maxID, "Некорректный уровень топлива. Откройте /menu и выберите значение кнопкой.", nil)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("fuel update requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "inspection.update")
	if err != nil {
		return err
	}
	result, err := p.Commands.InspectionUpdate(ctx, actor, inspectionID, version, dataapi.InspectionUpdateInput{FuelLevel: &level}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Осмотр изменился или hold истёк. Обновите /menu.", nil)
		}
		return err
	}
	updated, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
	if err != nil || updated.ID != inspectionID || updated.Version != version+1 || updated.FuelLevel == nil || *updated.FuelLevel != level {
		return errors.New("fuel update returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Топливо %d%% сохранено. Продолжите осмотр через /menu.", level), nil)
}
