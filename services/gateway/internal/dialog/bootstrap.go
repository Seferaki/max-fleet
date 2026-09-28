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

type Reader interface {
	Me(context.Context, string) (dataapi.Me, error)
	State(context.Context, string) (dataapi.CurrentState, error)
	Vehicles(context.Context, string, dataapi.VehicleFilter) (dataapi.Page[dataapi.Vehicle], error)
}

// Bootstrap handles only entry/menu events. Every other accepted event remains
// durable and unacknowledged until its dialog flow is implemented.
type Bootstrap struct {
	Data Reader
	MAX  maxsdk.Transport
}

func (p Bootstrap) Handle(ctx context.Context, item dataapi.InboxClaimItem) error {
	pageNumber, catalog := catalogPage(item.Event)
	if !catalog && !isMenuEvent(item.Event) {
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
		message, err := p.catalogText(ctx, actor, *me.Employee, state, pageNumber)
		if err != nil {
			return err
		}
		_, err = p.MAX.SendText(ctx, maxID, message)
		return err
	}
	_, err = p.MAX.SendText(ctx, maxID, menuText(*me.Employee, state))
	return err
}

func catalogPage(event dataapi.NormalizedEvent) (int, bool) {
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

func (p Bootstrap) catalogText(ctx context.Context, actor string, employee dataapi.Employee, state dataapi.CurrentState, wanted int) (string, error) {
	if !employee.CanStartTrip || state.Trip != nil || state.Checkout != nil {
		return "Сейчас нельзя начать оформление другой машины. Откройте /menu, чтобы продолжить текущий сценарий.", nil
	}
	available := true
	cursor := ""
	var page dataapi.Page[dataapi.Vehicle]
	for number := 1; number <= wanted; number++ {
		var err error
		page, err = p.Data.Vehicles(ctx, actor, dataapi.VehicleFilter{Available: &available, Limit: 5, Cursor: cursor})
		if err != nil {
			return "", err
		}
		if number < wanted {
			if page.NextCursor == nil {
				return "Список изменился. Обновите: /cars", nil
			}
			cursor = *page.NextCursor
		}
	}
	if len(page.Items) == 0 {
		return "Сейчас нет доступных автомобилей. Попробуйте обновить список позже.", nil
	}
	lines := []string{fmt.Sprintf("Доступные автомобили · страница %d", wanted)}
	for _, vehicle := range page.Items {
		lines = append(lines, fmt.Sprintf("%s · %s %s", oneLine(vehicle.Plate), oneLine(vehicle.Make), oneLine(vehicle.Model)))
	}
	if wanted > 1 {
		lines = append(lines, fmt.Sprintf("Назад: /cars %d", wanted-1))
	}
	if page.NextCursor != nil && wanted < 20 {
		lines = append(lines, fmt.Sprintf("Далее: /cars %d", wanted+1))
	}
	lines = append(lines, "Обновить: /cars")
	return strings.Join(lines, "\n"), nil
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func isMenuEvent(event dataapi.NormalizedEvent) bool {
	if event.EventType == "bot_started" && event.Payload.Kind == "start" {
		return true
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(*event.Payload.Text))
	return command == "/start" || command == "/menu"
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
