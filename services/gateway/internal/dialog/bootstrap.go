package dialog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type Reader interface {
	Me(context.Context, string) (dataapi.Me, error)
	State(context.Context, string) (dataapi.CurrentState, error)
	Vehicles(context.Context, string, dataapi.VehicleFilter) (dataapi.Page[dataapi.Vehicle], error)
	Vehicle(context.Context, string, string) (dataapi.Vehicle, error)
	PreviousInspection(context.Context, string, string) (dataapi.Inspection, error)
}

// Bootstrap handles only entry/menu events. Every other accepted event remains
// durable and unacknowledged until its dialog flow is implemented.
type Bootstrap struct {
	Data     Reader
	MAX      maxsdk.Transport
	Location *time.Location
}

var vehicleIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (p Bootstrap) Handle(ctx context.Context, item dataapi.InboxClaimItem) error {
	pageNumber, catalog := catalogPage(item.Event)
	vehicleID, expectedVersion, card := cardTarget(item.Event)
	previousVehicleID, previous := previousTarget(item.Event)
	if !catalog && !card && !previous && !isMenuEvent(item.Event) {
		return inboxworker.ErrDeferred
	}
	if p.Data == nil || p.MAX == nil {
		return errors.New("dialog bootstrap is not configured")
	}
	actor := item.Event.ActorMaxUserID
	maxID, err := strconv.ParseInt(actor, 10, 64)
	if err != nil || maxID <= 0 {
		return errors.New("invalid dialog actor")
	}
	if item.Event.EventType == "message_callback" && item.Event.CallbackID != nil {
		// Callback acknowledgement is best effort: retrying this event after a
		// successful message send would duplicate the dialog response.
		_ = p.MAX.AnswerCallback(ctx, *item.Event.CallbackID)
	}
	me, err := p.Data.Me(ctx, actor)
	if err != nil {
		return err
	}
	if !me.Allowed || me.Employee == nil {
		_, err = p.MAX.SendText(ctx, maxID, "Доступ ещё не выдан. Передайте ответственному за автопарк ваш ID: "+actor)
		return err
	}
	state, err := p.Data.State(ctx, actor)
	if err != nil {
		return err
	}
	if catalog {
		message, rows, err := p.catalogView(ctx, actor, *me.Employee, state, pageNumber)
		if err != nil {
			return err
		}
		return p.sendView(ctx, maxID, message, rows)
	}
	if card {
		message := "Некорректная ссылка на автомобиль. Откройте /cars."
		rows := [][]maxsdk.Button{{{Text: "К списку", Payload: "cars:1"}}}
		if vehicleIDPattern.MatchString(vehicleID) {
			vehicle, readErr := p.Data.Vehicle(ctx, actor, vehicleID)
			if readErr != nil {
				var apiErr *dataapi.APIError
				if !errors.As(readErr, &apiErr) || apiErr.Status != 404 {
					return readErr
				}
				message = "Автомобиль больше не доступен по этой ссылке. Обновите /cars."
			} else {
				message = cardText(vehicle, p.Location)
				rows = append([][]maxsdk.Button{{{Text: "Предыдущий осмотр", Payload: "prev:" + vehicle.ID}}}, rows...)
				if expectedVersion > 0 && vehicle.Version != expectedVersion {
					message = "Данные автомобиля изменились. Ниже актуальная карточка.\n" + message
				}
			}
		}
		return p.sendView(ctx, maxID, message, rows)
	}
	if previous {
		message := "Некорректная ссылка на предыдущий осмотр. Откройте /cars."
		if vehicleIDPattern.MatchString(previousVehicleID) {
			inspection, readErr := p.Data.PreviousInspection(ctx, actor, previousVehicleID)
			if readErr != nil {
				var apiErr *dataapi.APIError
				if !errors.As(readErr, &apiErr) || apiErr.Status != 404 {
					return readErr
				}
				message = "Подтверждённого предыдущего осмотра пока нет."
			} else {
				message = previousInspectionText(inspection, p.Location)
			}
		}
		return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "К списку", Payload: "cars:1"}}})
	}
	return p.sendView(ctx, maxID, menuText(*me.Employee, state), menuRows(*me.Employee, state))
}

func previousTarget(event dataapi.NormalizedEvent) (string, bool) {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		payload := *event.Payload.CallbackData
		if strings.HasPrefix(payload, "prev:") {
			return strings.TrimPrefix(payload, "prev:"), true
		}
	}
	return "", false
}

func previousInspectionText(inspection dataapi.Inspection, location *time.Location) string {
	if inspection.Phase != "after" || inspection.Status != "finalized" {
		return "Подтверждённого предыдущего осмотра пока нет."
	}
	lines := []string{"Предыдущий завершённый осмотр", "Состояние на " + formatMoment(inspection.UpdatedAt, location)}
	if inspection.FuelLevel == nil {
		lines = append(lines, "Топливо: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Топливо: %d%%", *inspection.FuelLevel))
	}
	if inspection.OdometerKM == nil {
		lines = append(lines, "Пробег: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Пробег: %d км", *inspection.OdometerKM))
	}
	lines = append(lines, fmt.Sprintf("Фото: %d из 8", len(inspection.OccupiedSlots)))
	return strings.Join(lines, "\n")
}

func (p Bootstrap) sendView(ctx context.Context, maxID int64, text string, rows [][]maxsdk.Button) error {
	if len(rows) == 0 {
		_, err := p.MAX.SendText(ctx, maxID, text)
		return err
	}
	_, err := p.MAX.SendButtons(ctx, maxID, text, rows)
	return err
}

func cardTarget(event dataapi.NormalizedEvent) (string, int64, bool) {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		payload := *event.Payload.CallbackData
		if !strings.HasPrefix(payload, "car:") {
			return "", 0, false
		}
		parts := strings.Split(payload, ":")
		if len(parts) != 3 {
			return "", 0, true
		}
		version, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || version < 1 {
			return "", 0, true
		}
		return parts[1], version, true
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return "", 0, false
	}
	command := strings.TrimSpace(*event.Payload.Text)
	if !strings.HasPrefix(command, "/car ") {
		return "", 0, false
	}
	return strings.TrimSpace(strings.TrimPrefix(command, "/car ")), 0, true
}

func cardText(vehicle dataapi.Vehicle, location *time.Location) string {
	lines := []string{
		fmt.Sprintf("%s · %s %s", oneLine(vehicle.Plate), oneLine(vehicle.Make), oneLine(vehicle.Model)),
		"Статус: " + oneLine(vehicle.Status),
	}
	if vehicle.CurrentParking == nil {
		lines = append(lines, "Место парковки: Не указано")
	} else {
		parking := vehicle.CurrentParking
		lines = append(lines, fmt.Sprintf("Место парковки: %.6f, %.6f", parking.Latitude, parking.Longitude))
		if parking.Landmark != nil && strings.TrimSpace(*parking.Landmark) != "" {
			lines = append(lines, "Ориентир: "+oneLine(*parking.Landmark))
		}
		lines = append(lines, "Место подтверждено: "+formatMoment(parking.ConfirmedAt, location))
	}
	if vehicle.CurrentFuel == nil {
		lines = append(lines, "Топливо: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Топливо: %d%%", *vehicle.CurrentFuel))
	}
	if vehicle.FuelConfirmedAt != nil {
		lines = append(lines, "Топливо обновлено: "+formatMoment(*vehicle.FuelConfirmedAt, location))
	}
	if vehicle.CurrentOdometerKM == nil {
		lines = append(lines, "Пробег: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Пробег: %d км", *vehicle.CurrentOdometerKM))
	}
	if vehicle.OdometerConfirmedAt != nil {
		lines = append(lines, "Пробег обновлён: "+formatMoment(*vehicle.OdometerConfirmedAt, location))
	}
	lines = append(lines, "Описание: "+valueOrUnknown(vehicle.Description))
	if len(vehicle.KnownNonblockingIssues) == 0 {
		lines = append(lines, "Известные замечания: Нет")
	} else {
		issues := make([]string, 0, len(vehicle.KnownNonblockingIssues))
		for _, issue := range vehicle.KnownNonblockingIssues {
			issues = append(issues, oneLine(issue))
		}
		lines = append(lines, "Известные замечания: "+strings.Join(issues, "; "))
	}
	lines = append(lines, "Ключи: "+valueOrUnknown(vehicle.KeyInstructions), "К списку: /cars")
	return strings.Join(lines, "\n")
}

func valueOrUnknown(value string) string {
	value = oneLine(value)
	if value == "" {
		return "Не указано"
	}
	return value
}

func formatMoment(moment time.Time, location *time.Location) string {
	if moment.IsZero() {
		return "Не указано"
	}
	if location == nil {
		location = time.UTC
	}
	return moment.In(location).Format("02.01.2006 15:04 MST")
}

func catalogPage(event dataapi.NormalizedEvent) (int, bool) {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		payload := *event.Payload.CallbackData
		if !strings.HasPrefix(payload, "cars:") {
			return 0, false
		}
		page, err := strconv.Atoi(strings.TrimPrefix(payload, "cars:"))
		return page, err == nil && page >= 1 && page <= 20
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return 0, false
	}
	command := strings.ToLower(strings.TrimSpace(*event.Payload.Text))
	if command == "доступные автомобили" || command == "/cars" {
		return 1, true
	}
	if !strings.HasPrefix(command, "/cars ") {
		return 0, false
	}
	page, err := strconv.Atoi(strings.TrimPrefix(command, "/cars "))
	return page, err == nil && page >= 1 && page <= 20
}

func (p Bootstrap) catalogView(ctx context.Context, actor string, employee dataapi.Employee, state dataapi.CurrentState, wanted int) (string, [][]maxsdk.Button, error) {
	if !employee.CanStartTrip || state.Trip != nil || state.Checkout != nil {
		return "Сейчас нельзя начать оформление другой машины. Откройте /menu, чтобы продолжить текущий сценарий.", [][]maxsdk.Button{{{Text: "В меню", Payload: "menu"}}}, nil
	}
	available := true
	cursor := ""
	var page dataapi.Page[dataapi.Vehicle]
	for number := 1; number <= wanted; number++ {
		var err error
		page, err = p.Data.Vehicles(ctx, actor, dataapi.VehicleFilter{Available: &available, Limit: 5, Cursor: cursor})
		if err != nil {
			return "", nil, err
		}
		if number < wanted {
			if page.NextCursor == nil {
				return "Список изменился. Обновите: /cars", [][]maxsdk.Button{{{Text: "Обновить", Payload: "cars:1"}}}, nil
			}
			cursor = *page.NextCursor
		}
	}
	if len(page.Items) == 0 {
		return "Сейчас нет доступных автомобилей. Попробуйте обновить список позже.", [][]maxsdk.Button{{{Text: "Обновить", Payload: "cars:1"}}}, nil
	}
	lines := []string{fmt.Sprintf("Доступные автомобили · страница %d", wanted)}
	rows := make([][]maxsdk.Button, 0, len(page.Items)+1)
	for _, vehicle := range page.Items {
		label := fmt.Sprintf("%s · %s %s", oneLine(vehicle.Plate), oneLine(vehicle.Make), oneLine(vehicle.Model))
		lines = append(lines, label)
		rows = append(rows, []maxsdk.Button{{Text: shortLabel(label), Payload: fmt.Sprintf("car:%s:%d", vehicle.ID, vehicle.Version)}})
	}
	controls := []maxsdk.Button{{Text: "Обновить", Payload: "cars:1"}}
	if wanted > 1 {
		lines = append(lines, fmt.Sprintf("Назад: /cars %d", wanted-1))
		controls = append(controls, maxsdk.Button{Text: "Назад", Payload: fmt.Sprintf("cars:%d", wanted-1)})
	}
	if page.NextCursor != nil && wanted < 20 {
		lines = append(lines, fmt.Sprintf("Далее: /cars %d", wanted+1))
		controls = append(controls, maxsdk.Button{Text: "Далее", Payload: fmt.Sprintf("cars:%d", wanted+1)})
	}
	lines = append(lines, "Обновить: /cars")
	rows = append(rows, controls)
	return strings.Join(lines, "\n"), rows, nil
}

func shortLabel(value string) string {
	runes := []rune(value)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return value
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func isMenuEvent(event dataapi.NormalizedEvent) bool {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		return *event.Payload.CallbackData == "menu"
	}
	if event.EventType == "bot_started" && event.Payload.Kind == "start" {
		return true
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(*event.Payload.Text))
	return command == "/start" || command == "/menu"
}

func menuRows(employee dataapi.Employee, state dataapi.CurrentState) [][]maxsdk.Button {
	if employee.CanStartTrip && state.Trip == nil && state.Checkout == nil {
		return [][]maxsdk.Button{{{Text: "Доступные автомобили", Payload: "cars:1"}}}
	}
	return nil
}

func menuText(employee dataapi.Employee, state dataapi.CurrentState) string {
	lines := []string{"MAX Fleet"}
	if state.Trip != nil {
		lines = append(lines, "Текущая поездка")
	} else {
		if state.Checkout != nil {
			lines = append(lines, "Продолжить оформление")
		} else if employee.CanStartTrip {
			lines = append(lines, "Доступные автомобили")
		}
	}
	lines = append(lines, "Мои поездки", "Правила и помощь")
	if employee.Role == "admin" {
		lines = append(lines, "Управление автопарком")
	}
	return strings.Join(lines, "\n")
}
