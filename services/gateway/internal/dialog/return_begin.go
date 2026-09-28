package dialog

import (
	"context"
	"errors"
	"fmt"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func returnDraftMatches(state dataapi.CurrentState, employee dataapi.Employee, tripID string) bool {
	return state.Trip != nil && state.Trip.ID == tripID && state.Trip.EmployeeID == employee.ID && state.Trip.Status == "returning" &&
		state.Return != nil && state.Return.TripID == tripID && state.Return.Status == "draft" && state.Return.Inspection.Phase == "after" &&
		state.Trip.ReturnID != nil && *state.Trip.ReturnID == state.Return.ID
}

func (p Bootstrap) savedReturnDraft(ctx context.Context, maxID int64, state dataapi.CurrentState) error {
	if state.Return == nil || state.Trip == nil {
		return errors.New("return draft missing from state")
	}
	return p.sendView(ctx, maxID, "Оформление возврата начато. Поездка и занятость автомобиля сохраняются до завершения возврата. Продолжите через /menu.", [][]maxsdk.Button{{{Text: "Текущая поездка", Payload: "trip:" + state.Trip.ID}}})
}

func (p Bootstrap) beginReturn(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, confirm bool) error {
	if !vehicleIDPattern.MatchString(tripID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка возврата повреждена. Откройте /menu.", nil)
	}
	if confirm && returnDraftMatches(state, employee, tripID) {
		return p.savedReturnDraft(ctx, maxID, state)
	}
	trip := state.Trip
	if trip == nil || trip.ID != tripID || trip.EmployeeID != employee.ID || trip.Version != version || trip.Status != "active" || trip.ReturnID != nil || state.Return != nil {
		return p.sendView(ctx, maxID, "Поездка или возврат изменились. Обновите /menu.", nil)
	}
	if !confirm {
		vehicle, err := p.Data.Vehicle(ctx, actor, trip.VehicleID)
		if err != nil {
			return err
		}
		message := fmt.Sprintf("Вы припарковали %s и готовы оформить возврат? Поездка и занятость машины сохраняются до итогового подтверждения.", oneLine(vehicle.Plate))
		return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Да, оформить возврат", Payload: fmt.Sprintf("return-confirm:%s:%d", trip.ID, trip.Version)}}, {{Text: "Продолжить поездку", Payload: "trip:" + trip.ID}}})
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("begin return requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "trip.begin_return")
	if err != nil {
		return err
	}
	result, err := p.Commands.TripBeginReturn(ctx, actor, trip.ID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			current, readErr := p.Data.State(ctx, actor)
			if readErr != nil {
				return readErr
			}
			if returnDraftMatches(current, employee, tripID) {
				return p.savedReturnDraft(ctx, maxID, current)
			}
			return p.sendView(ctx, maxID, "Начало возврата не подтверждено. Проверьте поездку через /menu.", nil)
		}
		return err
	}
	draft, err := dataapi.DecodeAggregate[dataapi.Return](result)
	if err != nil || !vehicleIDPattern.MatchString(draft.ID) || draft.TripID != trip.ID || draft.Status != "draft" || draft.Step != "math" || draft.Inspection.Phase != "after" || len(draft.Inspection.OccupiedSlots) != 0 || len(draft.Inspection.MissingSlots) != 8 {
		return errors.New("begin return returned invalid draft")
	}
	current, err := p.Data.State(ctx, actor)
	if err != nil {
		return err
	}
	if !returnDraftMatches(current, employee, tripID) || current.Return.ID != draft.ID {
		return errors.New("begin return state disagrees with command result")
	}
	return p.savedReturnDraft(ctx, maxID, current)
}
