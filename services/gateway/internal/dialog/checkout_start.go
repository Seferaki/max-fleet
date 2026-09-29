package dialog

import (
	"context"
	"errors"
	"fmt"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func readyForCheckoutStart(checkout dataapi.Checkout) bool {
	i := checkout.Inspection
	return checkout.Status == "holding" && checkout.Step == "inspection" && checkout.IntentConfirmedAt != nil && checkout.RulesAcceptedAt != nil && checkout.RulesVersionID != nil &&
		i.Phase == "before" && i.Status == "draft" && i.PhotosConfirmedAt != nil && len(i.OccupiedSlots) == 8 && len(i.MissingSlots) == 0 &&
		i.FuelLevel != nil && i.OdometerKM != nil && i.NewDamage != nil && !*i.NewDamage && checkout.NoNewIssues != nil && *checkout.NoNewIssues
}

func (p Bootstrap) activeTripCard(ctx context.Context, maxID int64, trip dataapi.Trip) error {
	if !vehicleIDPattern.MatchString(trip.ID) || trip.Status != "active" || trip.BeforeInspection.Status != "finalized" || len(trip.BeforeInspection.OccupiedSlots) != 8 {
		return errors.New("checkout start returned invalid active trip")
	}
	message := fmt.Sprintf("Поездка началась.\nАвтомобиль: %s\nНачало: %s\nФото до поездки: 8/8\nПоездка: %s", trip.VehicleID, formatMoment(trip.StartedAt, p.Location), trip.ID)
	return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Открыть поездку", Payload: "trip:" + trip.ID}}, {{Text: "Завершить поездку", Payload: fmt.Sprintf("return-intent:%s:%d", trip.ID, trip.Version)}}})
}

func (p Bootstrap) checkoutSummaryStart(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, checkoutID string, version int64, start bool) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка оформления повреждена. Откройте /menu.", nil)
	}
	// A committed start removes the hold from /state. Replaying the same inbox
	// event must display the verified trip even if the MAX reply was lost.
	if start && state.Trip != nil && state.Trip.CheckoutID == checkoutID && state.Trip.EmployeeID == employee.ID {
		return p.activeTripCard(ctx, maxID, *state.Trip)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID || checkout.EmployeeID != employee.ID || checkout.Version != version || !readyForCheckoutStart(*checkout) || state.Trip != nil {
		return p.sendView(ctx, maxID, "Оформление изменилось или hold истёк. Обновите /menu.", nil)
	}
	if !start {
		vehicle, err := p.Data.Vehicle(ctx, actor, checkout.VehicleID)
		if err != nil {
			return err
		}
		if vehicle.Status != "holding" || vehicle.NeedsReview || vehicle.ManualBlocked {
			return p.sendView(ctx, maxID, "Автомобиль больше недоступен. Обновите /menu.", nil)
		}
		message := fmt.Sprintf("Проверьте перед выездом · %s\nФото до: 8/8, комплект подтверждён\nТопливо: %d%%\nПробег: %d км\nНовых замечаний нет\nПравила приняты\nHold до: %s\nНажимая «Начать поездку», подтверждаю достоверность осмотра и принимаю автомобиль.", oneLine(vehicle.Plate), *checkout.Inspection.FuelLevel, *checkout.Inspection.OdometerKM, formatMoment(checkout.ExpiresAt, p.Location))
		return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Начать поездку", Payload: fmt.Sprintf("checkout-start:%s:%d", checkout.ID, checkout.Version)}}, {{Text: "Вернуться к осмотру", Payload: "menu"}}})
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("checkout start requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.start")
	if err != nil {
		return err
	}
	lease := &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}
	result, err := p.Commands.CheckoutStart(ctx, actor, checkout.ID, version, key, lease)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404 || apiErr.Status == 422) {
			current, readErr := p.Data.State(ctx, actor)
			if readErr != nil {
				return readErr
			}
			if current.Trip != nil && current.Trip.CheckoutID == checkoutID && current.Trip.EmployeeID == employee.ID {
				return p.activeTripCard(ctx, maxID, *current.Trip)
			}
			return p.sendView(ctx, maxID, "Начало поездки не подтверждено. Проверьте состояние через /menu перед выездом.", nil)
		}
		return err
	}
	trip, err := dataapi.DecodeAggregate[dataapi.Trip](result)
	if err != nil || trip.CheckoutID != checkoutID || trip.EmployeeID != employee.ID || trip.VehicleID != checkout.VehicleID {
		return errors.New("checkout start returned mismatched trip")
	}
	return p.activeTripCard(ctx, maxID, trip)
}
