package dialog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type tripPhotoReader interface {
	Trip(context.Context, string, string) (dataapi.Trip, error)
	MyTrips(context.Context, string, int, string) (dataapi.Page[dataapi.Trip], error)
	AdminTrips(context.Context, string, dataapi.AdminTripFilter) (dataapi.Page[dataapi.Trip], error)
	TripInspectionPhoto(context.Context, string, string, string, int) (dataapi.AssetContent, error)
}

type tripViewRequest struct {
	recognized bool
	kind       string
	id         string
	phase      string
	page       int
	slot       int
	version    int64
}

func parseTripView(event dataapi.NormalizedEvent) tripViewRequest {
	var value string
	if event.EventType == "message_created" && event.Payload.Kind == "text" && event.Payload.Text != nil {
		value = strings.TrimSpace(*event.Payload.Text)
		switch {
		case value == "/trips":
			return tripViewRequest{recognized: true, kind: "mine", page: 1}
		case value == "/admintrips":
			return tripViewRequest{recognized: true, kind: "admin", page: 1}
		case strings.HasPrefix(value, "/trip "):
			return tripViewRequest{recognized: true, kind: "detail", id: strings.TrimSpace(strings.TrimPrefix(value, "/trip "))}
		default:
			return tripViewRequest{}
		}
	}
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return tripViewRequest{}
	}
	value = *event.Payload.CallbackData
	parts := strings.Split(value, ":")
	switch parts[0] {
	case "trip-list":
		request := tripViewRequest{recognized: true}
		if len(parts) == 3 && (parts[1] == "mine" || parts[1] == "admin") {
			request.kind, request.page = parts[1], parsePositiveInt(parts[2])
		}
		return request
	case "trip":
		request := tripViewRequest{recognized: true}
		if len(parts) == 2 {
			request.kind, request.id = "detail", parts[1]
		}
		return request
	case "trip-issues":
		request := tripViewRequest{recognized: true}
		if len(parts) == 4 {
			request.kind, request.id = "issues", parts[1]
			request.version, _ = strconv.ParseInt(parts[2], 10, 64)
			request.page = parsePositiveInt(parts[3])
		}
		return request
	case "photo-phase", "photo-view":
		request := tripViewRequest{recognized: true}
		if len(parts) == 4 || len(parts) == 5 && parts[0] == "photo-view" {
			request.kind, request.id, request.phase = parts[0], parts[1], parts[3]
			request.version, _ = strconv.ParseInt(parts[2], 10, 64)
			if len(parts) == 5 {
				request.slot = parsePositiveInt(parts[4])
			}
		}
		return request
	}
	return tripViewRequest{}
}

func parsePositiveInt(value string) int {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0
	}
	return number
}

func (p Bootstrap) showTripView(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, request tripViewRequest) error {
	reader, ok := p.Data.(tripPhotoReader)
	if !ok {
		return errors.New("dialog trip photo reader is not configured")
	}
	if request.kind == "mine" || request.kind == "admin" {
		return p.showTripList(ctx, actor, maxID, employee, reader, request.kind, request.page)
	}
	if !vehicleIDPattern.MatchString(request.id) {
		return p.sendView(ctx, maxID, "Некорректная кнопка поездки. Откройте /trips.", nil)
	}
	trip, err := reader.Trip(ctx, actor, request.id)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Поездка недоступна. Обновите /trips.", nil)
		}
		return err
	}
	if request.kind == "detail" {
		return p.showTripDetail(ctx, actor, maxID, employee, trip)
	}
	if request.version < 1 || trip.Version != request.version {
		return p.sendView(ctx, maxID, "Данные поездки изменились. Откройте её снова через /trips.", nil)
	}
	if request.kind == "issues" {
		return p.showTripIssues(ctx, maxID, trip, request.page)
	}
	inspection, available := visibleTripInspection(trip, request.phase)
	if !available {
		return p.sendView(ctx, maxID, "Этот осмотр пока недоступен. Откройте поездку снова через /trips.", nil)
	}
	if request.kind == "photo-phase" {
		rows := make([][]maxsdk.Button, 0, 9)
		for _, slot := range inspection.OccupiedSlots {
			if slot < 1 || slot > 8 {
				return errors.New("data-api: invalid inspection slots")
			}
			rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("%d. %s", slot, photoAngles[slot-1]), Payload: fmt.Sprintf("photo-view:%s:%d:%s:%d", trip.ID, trip.Version, request.phase, slot)}})
		}
		rows = append(rows, []maxsdk.Button{{Text: "К поездке", Payload: "trip:" + trip.ID}})
		return p.sendView(ctx, maxID, "Выберите ракурс "+tripPhaseLabel(request.phase)+" ("+strconv.Itoa(len(inspection.OccupiedSlots))+"/8).", rows)
	}
	if request.kind != "photo-view" || request.slot < 1 || request.slot > 8 || !containsSlot(inspection.OccupiedSlots, request.slot) {
		return p.sendView(ctx, maxID, "Ракурс недоступен. Откройте поездку снова через /trips.", nil)
	}
	content, err := reader.TripInspectionPhoto(ctx, actor, trip.ID, request.phase, request.slot)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Ракурс недоступен. Откройте поездку снова через /trips.", nil)
		}
		return err
	}
	sender, ok := p.MAX.(maxsdk.PhotoSender)
	if !ok {
		return errors.New("MAX photo sender is not configured")
	}
	_, err = sender.SendImage(ctx, maxID, fmt.Sprintf("Поездка %s · %s · %d/8: %s", trip.ID, tripPhaseLabel(request.phase), request.slot, photoAngles[request.slot-1]), content.ContentType, content.Bytes)
	return err
}

func (p Bootstrap) showTripList(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, reader tripPhotoReader, scope string, page int) error {
	if page < 1 || page > 20 || scope == "admin" && employee.Role != "admin" {
		return p.sendView(ctx, maxID, "Список поездок недоступен. Откройте /trips.", nil)
	}
	var trips dataapi.Page[dataapi.Trip]
	var err error
	cursor := ""
	for index := 1; index <= page; index++ {
		if scope == "admin" {
			trips, err = reader.AdminTrips(ctx, actor, dataapi.AdminTripFilter{Limit: 8, Cursor: cursor})
		} else {
			trips, err = reader.MyTrips(ctx, actor, 5, cursor)
		}
		if err != nil {
			return err
		}
		if index < page {
			if trips.NextCursor == nil {
				return p.sendView(ctx, maxID, "Больше поездок нет. Откройте /trips.", nil)
			}
			cursor = *trips.NextCursor
		}
	}
	title := "Мои поездки"
	if scope == "admin" {
		title = "Поездки автопарка"
	}
	rows := make([][]maxsdk.Button, 0, 10)
	for _, trip := range trips.Items {
		if !vehicleIDPattern.MatchString(trip.ID) {
			return errors.New("data-api: invalid trip list projection")
		}
		rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("%s · %s", shortLabel(trip.ID[:8]), shortLabel(trip.Status)), Payload: "trip:" + trip.ID}})
	}
	controls := []maxsdk.Button{}
	if page > 1 {
		controls = append(controls, maxsdk.Button{Text: "Назад", Payload: fmt.Sprintf("trip-list:%s:%d", scope, page-1)})
	}
	if trips.NextCursor != nil && page < 20 {
		controls = append(controls, maxsdk.Button{Text: "Далее", Payload: fmt.Sprintf("trip-list:%s:%d", scope, page+1)})
	}
	if len(controls) != 0 {
		rows = append(rows, controls)
	}
	if len(rows) == 0 {
		return p.sendView(ctx, maxID, title+": пока пусто.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("%s · страница %d", title, page), rows)
}

func (p Bootstrap) showTripDetail(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, trip dataapi.Trip) error {
	rows := [][]maxsdk.Button{}
	if trip.EmployeeID == employee.ID && trip.Status == "active" {
		rows = append(rows, []maxsdk.Button{{Text: "Сообщить проблему", Payload: fmt.Sprintf("trip-issue:%s:%d", trip.ID, trip.Version)}})
		rows = append(rows, []maxsdk.Button{{Text: "Завершить поездку", Payload: fmt.Sprintf("return-intent:%s:%d", trip.ID, trip.Version)}})
	}
	if _, ok := visibleTripInspection(trip, "before"); ok {
		rows = append(rows, []maxsdk.Button{{Text: "Фото до", Payload: fmt.Sprintf("photo-phase:%s:%d:before", trip.ID, trip.Version)}})
	}
	if _, ok := visibleTripInspection(trip, "after"); ok {
		rows = append(rows, []maxsdk.Button{{Text: "Фото после", Payload: fmt.Sprintf("photo-phase:%s:%d:after", trip.ID, trip.Version)}})
	}
	if len(trip.Issues) > 0 {
		rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("Замечания (%d)", len(trip.Issues)), Payload: fmt.Sprintf("trip-issues:%s:%d:1", trip.ID, trip.Version)}})
	}
	rows = append(rows, []maxsdk.Button{{Text: "Мои поездки", Payload: "trip-list:mine:1"}})
	vehicleLabel := trip.VehicleID
	if vehicleIDPattern.MatchString(trip.VehicleID) {
		vehicle, err := p.Data.Vehicle(ctx, actor, trip.VehicleID)
		if err != nil {
			return err
		}
		vehicleLabel = oneLine(vehicle.Plate)
	}
	message := fmt.Sprintf("Поездка %s\nСтатус: %s\nАвтомобиль: %s", trip.ID, oneLine(trip.Status), vehicleLabel)
	if trip.Status == "closed_by_admin" {
		message += "\nЗакрыто администратором; часть данных возврата может отсутствовать."
		if len(trip.MissingData) > 0 {
			message += " Не хватает: " + shortLabel(oneLine(strings.Join(trip.MissingData, ", "))) + "."
		}
	}
	if !trip.StartedAt.IsZero() {
		elapsed := max(0, int(time.Since(trip.StartedAt).Minutes()))
		message += fmt.Sprintf("\nНачало: %s\nДлительность: %d ч %d мин", formatMoment(trip.StartedAt, p.Location), elapsed/60, elapsed%60)
	}
	return p.sendView(ctx, maxID, message, rows)
}

func (p Bootstrap) showTripIssues(ctx context.Context, maxID int64, trip dataapi.Trip, page int) error {
	if page < 1 || page > 20 || (page-1)*5 >= len(trip.Issues) {
		return p.sendView(ctx, maxID, "Страница замечаний недоступна. Откройте поездку снова через /trips.", nil)
	}
	start := (page - 1) * 5
	end := min(start+5, len(trip.Issues))
	lines := []string{fmt.Sprintf("Замечания поездки %s · страница %d", trip.ID, page)}
	for index, issue := range trip.Issues[start:end] {
		lines = append(lines, fmt.Sprintf("%d. %s · %s: %s", start+index+1, shortLabel(oneLine(issue.Category)), shortLabel(oneLine(issue.Status)), shortLabel(oneLine(issue.Description))))
	}
	controls := []maxsdk.Button{}
	if page > 1 {
		controls = append(controls, maxsdk.Button{Text: "Назад", Payload: fmt.Sprintf("trip-issues:%s:%d:%d", trip.ID, trip.Version, page-1)})
	}
	if end < len(trip.Issues) && page < 20 {
		controls = append(controls, maxsdk.Button{Text: "Далее", Payload: fmt.Sprintf("trip-issues:%s:%d:%d", trip.ID, trip.Version, page+1)})
	}
	rows := [][]maxsdk.Button{}
	if len(controls) > 0 {
		rows = append(rows, controls)
	}
	rows = append(rows, []maxsdk.Button{{Text: "К поездке", Payload: "trip:" + trip.ID}})
	return p.sendView(ctx, maxID, strings.Join(lines, "\n"), rows)
}

func visibleTripInspection(trip dataapi.Trip, phase string) (dataapi.Inspection, bool) {
	if phase == "before" && trip.BeforeInspection.Phase == "before" && trip.BeforeInspection.Status == "finalized" {
		return trip.BeforeInspection, true
	}
	if phase == "after" && (trip.Status == "completed" || trip.Status == "closed_by_admin") && trip.AfterInspection != nil && trip.AfterInspection.Phase == "after" && trip.AfterInspection.Status == "finalized" {
		return *trip.AfterInspection, true
	}
	return dataapi.Inspection{}, false
}

func tripPhaseLabel(phase string) string {
	if phase == "before" {
		return "до"
	}
	return "после"
}

func containsSlot(slots []int, slot int) bool {
	for _, candidate := range slots {
		if candidate == slot {
			return true
		}
	}
	return false
}
