package dialog

import (
	"context"
	"errors"
	"fmt"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type returnReader interface {
	Return(context.Context, string, string) (dataapi.Return, error)
}

func (p Bootstrap) cancelledReturn(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string) (bool, error) {
	if state.Trip == nil || state.Trip.EmployeeID != employee.ID || state.Trip.Status != "active" || state.Trip.ReturnID != nil || state.Return != nil {
		return false, nil
	}
	reader, ok := p.Data.(returnReader)
	if !ok {
		return false, errors.New("return reader is not configured")
	}
	draft, err := reader.Return(ctx, actor, returnID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return false, nil
		}
		return false, err
	}
	if draft.ID != returnID || draft.TripID != state.Trip.ID || draft.Status != "cancelled" || draft.Inspection.Status != "abandoned" {
		return false, nil
	}
	return true, p.sendView(ctx, maxID, "Возврат отменён. Поездка и занятость машины продолжаются. При следующем возврате потребуются новые фотографии и новая точка парковки.", [][]maxsdk.Button{{{Text: "Текущая поездка", Payload: "trip:" + state.Trip.ID}}})
}

func (p Bootstrap) cancelReturn(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string, version int64, confirm bool) error {
	if !vehicleIDPattern.MatchString(returnID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка отмены возврата повреждена. Откройте /menu.", nil)
	}
	if confirm {
		if done, err := p.cancelledReturn(ctx, actor, maxID, employee, state, returnID); done || err != nil {
			return err
		}
	}
	if !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.ID != returnID || state.Return.Version != version {
		return p.sendView(ctx, maxID, "Оформление возврата изменилось. Обновите /menu.", nil)
	}
	if !confirm {
		message := "Вернуться к поездке? Черновик возврата, его фотографии и точка парковки не перейдут в следующий возврат. Машина останется занятой вами."
		return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Да, продолжить поездку", Payload: fmt.Sprintf("return-cancel:%s:%d", returnID, version)}}, {{Text: "Продолжить возврат", Payload: "menu"}}})
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return cancellation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "return.cancel")
	if err != nil {
		return err
	}
	result, err := p.Commands.ReturnCancel(ctx, actor, returnID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			current, readErr := p.Data.State(ctx, actor)
			if readErr != nil {
				return readErr
			}
			if done, readErr := p.cancelledReturn(ctx, actor, maxID, employee, current, returnID); done || readErr != nil {
				return readErr
			}
			return p.sendView(ctx, maxID, "Отмена возврата не подтверждена. Проверьте поездку через /menu.", nil)
		}
		return err
	}
	cancelled, err := dataapi.DecodeAggregate[dataapi.Return](result)
	if err != nil || cancelled.ID != returnID || cancelled.TripID != state.Trip.ID || cancelled.Status != "cancelled" || cancelled.Inspection.Status != "abandoned" {
		return errors.New("return cancellation returned invalid draft")
	}
	current, err := p.Data.State(ctx, actor)
	if err != nil {
		return err
	}
	if done, err := p.cancelledReturn(ctx, actor, maxID, employee, current, returnID); done || err != nil {
		return err
	}
	return errors.New("return cancellation did not restore active trip")
}
