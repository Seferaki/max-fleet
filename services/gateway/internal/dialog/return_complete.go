package dialog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type completedTripReader interface {
	Trip(context.Context, string, string) (dataapi.Trip, error)
}

func returnSummaryMissing(state dataapi.CurrentState) ([]string, bool) {
	missing := []string{}
	if state.Return == nil || state.Trip == nil {
		return []string{"текущий возврат"}, false
	}
	inspection := state.Return.Inspection
	if inspection.NewDamage == nil || inspection.CabinClean == nil || inspection.ParkingAllowed == nil || inspection.KeysReturned == nil || inspection.CarLocked == nil {
		missing = append(missing, "анкета состояния")
	}
	if len(inspection.OccupiedSlots) != 8 || len(inspection.MissingSlots) != 0 || inspection.PhotosConfirmedAt == nil {
		missing = append(missing, "подтверждённые 8 фото после")
	}
	if inspection.FuelLevel == nil {
		missing = append(missing, "топливо")
	}
	if inspection.OdometerKM == nil {
		missing = append(missing, "пробег")
	}
	if state.Return.ParkingLocation == nil {
		missing = append(missing, "подтверждённая точка парковки")
	}
	for _, issue := range missingAfterIssueReports(inspection, state.Trip.Issues) {
		missing = append(missing, "замечание: "+issue)
	}
	unsafe := inspection.ParkingAllowed != nil && !*inspection.ParkingAllowed || inspection.KeysReturned != nil && !*inspection.KeysReturned || inspection.CarLocked != nil && !*inspection.CarLocked
	return missing, unsafe
}

func (p Bootstrap) savedCompletedReturn(ctx context.Context, actor string, maxID int64, completed dataapi.Return, returnID string) error {
	if completed.ID != returnID || completed.Status != "completed" || completed.Inspection.Phase != "after" || completed.Inspection.Status != "finalized" || completed.ParkingLocation == nil || len(completed.Inspection.OccupiedSlots) != 8 || completed.Inspection.PhotosConfirmedAt == nil {
		return errors.New("return completion returned invalid aggregate")
	}
	reader, ok := p.Data.(completedTripReader)
	if !ok {
		return errors.New("completed trip reader is not configured")
	}
	trip, err := reader.Trip(ctx, actor, completed.TripID)
	if err != nil {
		return err
	}
	if trip.ID != completed.TripID || trip.Status != "completed" || trip.ReturnID == nil || *trip.ReturnID != returnID || trip.ParkingLocation == nil || trip.EndedAt == nil {
		return errors.New("trip completion not confirmed")
	}
	message := "Возврат подтверждён. Поездка завершена; автомобиль доступен для следующей выдачи только по актуальному статусу автопарка."
	if len(trip.Issues) > 0 {
		message += " Открытые замечания требуют проверки ответственного; машина может оставаться недоступной."
	}
	return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Поездка", Payload: "trip:" + trip.ID}}})
}

func (p Bootstrap) returnSummaryComplete(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string, version int64, complete bool) error {
	if !vehicleIDPattern.MatchString(returnID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка возврата повреждена. Откройте /menu.", nil)
	}
	var key string
	var reader ownCommandReader
	if complete {
		if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
			return errors.New("return completion requires a durable inbox lease")
		}
		var err error
		key, err = inboxworker.CommandKey(item, "return.complete")
		if err != nil {
			return err
		}
		var ok bool
		reader, ok = p.Data.(ownCommandReader)
		if !ok {
			return errors.New("command result reader is not configured")
		}
		previous, readErr := reader.OwnCommandResult(ctx, actor, key, "return.complete")
		if readErr == nil {
			completed, err := dataapi.DecodeAggregate[dataapi.Return](previous)
			if err != nil {
				return err
			}
			return p.savedCompletedReturn(ctx, actor, maxID, completed, returnID)
		}
		var apiErr *dataapi.APIError
		if !errors.As(readErr, &apiErr) || apiErr.Status != 404 {
			return readErr
		}
	}
	if !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.ID != returnID || state.Return.Step != "checklist" || state.Return.Version != version {
		return p.sendView(ctx, maxID, "Возврат изменился. Откройте /menu; машина остаётся занятой до подтверждённого завершения.", nil)
	}
	missing, unsafe := returnSummaryMissing(state)
	if unsafe {
		return p.sendView(ctx, maxID, "Самостоятельное завершение возврата небезопасно: проблема с допустимостью парковки, ключами или закрытием. Сообщите ответственному через замечание и согласуйте дальнейшие действия. Машина остаётся занятой поездкой.", [][]maxsdk.Button{{{Text: "Сообщить проблему", Payload: fmt.Sprintf("return-issue:%s:%d", returnID, version)}}})
	}
	if len(missing) > 0 {
		return p.sendView(ctx, maxID, "Для завершения возврата ещё нужны: "+strings.Join(missing, ", ")+". Машина остаётся занятой поездкой.", [][]maxsdk.Button{{{Text: "К возврату", Payload: "menu"}}})
	}
	if !complete {
		inspection, parking := state.Return.Inspection, state.Return.ParkingLocation
		message := fmt.Sprintf("Проверьте возврат · поездка %s\nФото после: 8/8, подтверждены\nТопливо: %d%%\nПробег: %d км\nМесто: %.6f, %.6f (%s)\nПовреждение: %s; чистота: %s\nЗамечаний при возврате: %d\nКлючи возвращены, машина закрыта, парковка допустима. Нажимая «Завершить возврат», подтверждаю достоверность осмотра и места.", state.Trip.ID, *inspection.FuelLevel, *inspection.OdometerKM, parking.Latitude, parking.Longitude, parking.Source, yesNo(*inspection.NewDamage), yesNo(*inspection.CabinClean), countAfterIssues(inspection.ID, state.Trip.Issues))
		return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Завершить возврат", Payload: fmt.Sprintf("return-complete:%s:%d", returnID, version)}}, {{Text: "Исправить данные", Payload: "menu"}}})
	}
	result, err := p.Commands.ReturnComplete(ctx, actor, returnID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404 || apiErr.Status == 422) {
			return p.sendView(ctx, maxID, "Завершение не подтверждено. Проверьте /menu и текущее состояние поездки; не считайте машину свободной.", nil)
		}
		return err
	}
	completed, err := dataapi.DecodeAggregate[dataapi.Return](result)
	if err != nil {
		return err
	}
	return p.savedCompletedReturn(ctx, actor, maxID, completed, returnID)
}

func yesNo(value bool) string {
	if value {
		return "да"
	}
	return "нет"
}

func countAfterIssues(inspectionID string, issues []dataapi.Issue) int {
	count := 0
	for _, issue := range issues {
		if issue.Stage == "after" && issue.InspectionID != nil && *issue.InspectionID == inspectionID {
			count++
		}
	}
	return count
}
