package dialog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func returnGeoEvent(event dataapi.NormalizedEvent) bool {
	return event.EventType == "message_created" && event.Payload.Kind == "geo" && event.Payload.AttachmentCount == 1 && event.Payload.Latitude != nil && event.Payload.Longitude != nil
}

func returnDraftCoordinates(value string) (float64, float64, bool) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	lat, latErr := strconv.ParseFloat(parts[0], 64)
	lon, lonErr := strconv.ParseFloat(parts[1], 64)
	return lat, lon, latErr == nil && lonErr == nil && !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func returnLocationConversation(state dataapi.CurrentState) (*dataapi.Conversation, float64, float64, bool) {
	draft := state.Conversation
	if draft == nil || draft.Flow != "return_location" || draft.Context.TargetID == nil || draft.Context.ReturnID == nil || state.Return == nil || *draft.Context.TargetID != state.Return.ID || *draft.Context.ReturnID != state.Return.ID || draft.Context.TripID == nil || state.Trip == nil || *draft.Context.TripID != state.Trip.ID || draft.Context.VehicleID == nil || *draft.Context.VehicleID != state.Trip.VehicleID || draft.Context.DraftText == nil {
		return nil, 0, 0, false
	}
	lat, lon, ok := returnDraftCoordinates(*draft.Context.DraftText)
	return draft, lat, lon, ok
}

func (p Bootstrap) returnGeoPreview(ctx context.Context, maxID int64, state dataapi.CurrentState) error {
	draft, lat, lon, ok := returnLocationConversation(state)
	if !ok || draft.Step != "confirm" {
		return errors.New("return location preview has no confirmed draft")
	}
	message := fmt.Sprintf("Проверьте точку парковки: %.6f, %.6f. Отправленная геопозиция сама по себе не сохраняет место. Нажмите подтверждение только если машина действительно там.", lat, lon)
	rows := [][]maxsdk.Button{{{Text: "Подтвердить эту точку", Payload: fmt.Sprintf("return-geo-confirm:%s:%d", state.Return.ID, draft.Version)}}, {{Text: "Посмотреть точку", URL: parkingMapURL(dataapi.ParkingLocation{Latitude: lat, Longitude: lon})}}}
	return p.sendView(ctx, maxID, message, rows)
}

func (p Bootstrap) returnGeoDraft(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState) error {
	if !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" {
		return p.sendView(ctx, maxID, "Геопозиция пока не относится к текущему возврату. Откройте /menu.", nil)
	}
	if state.Conversation != nil && state.Conversation.Flow == "issue_after" && state.Conversation.Step == "collect_photos" && state.Conversation.Context.ReturnID != nil && *state.Conversation.Context.ReturnID == state.Return.ID {
		return p.sendView(ctx, maxID, "Сначала отправьте или исправьте черновик замечания через /menu, затем укажите место парковки.", nil)
	}
	lat, lon := *item.Event.Payload.Latitude, *item.Event.Payload.Longitude
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 || math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return p.sendView(ctx, maxID, "Геопозиция некорректна. Отправьте точку ещё раз или выберите её на карте.", nil)
	}
	coordinates := fmt.Sprintf("%.6f,%.6f", lat, lon)
	if draft, _, _, ok := returnLocationConversation(state); ok && draft.Step == "confirm" && draft.Context.DraftText != nil && *draft.Context.DraftText == coordinates {
		return p.returnGeoPreview(ctx, maxID, state)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return geo preview requires a durable inbox lease")
	}
	kind := "none"
	input := dataapi.ConversationSaveInput{Flow: "return_location", Step: "confirm", PendingInputKind: &kind, Context: dataapi.ConversationContext{TargetID: &state.Return.ID, ReturnID: &state.Return.ID, TripID: &state.Trip.ID, VehicleID: &state.Trip.VehicleID, DraftText: &coordinates}}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, input, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Точка или возврат изменились. Откройте /menu.", nil)
		}
		return err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Flow != "return_location" || saved.Step != "confirm" || saved.Version != state.ConversationVersion+1 || saved.Context.DraftText == nil || *saved.Context.DraftText != coordinates {
		return errors.New("return geo preview returned invalid conversation")
	}
	state.Conversation, state.ConversationVersion = &saved, saved.Version
	return p.returnGeoPreview(ctx, maxID, state)
}

func (p Bootstrap) returnGeoConfirm(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string, version int64) error {
	draft, lat, lon, ok := returnLocationConversation(state)
	if !ok || !vehicleIDPattern.MatchString(returnID) || version < 1 || state.Return.ID != returnID || !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.Step != "checklist" {
		return p.sendView(ctx, maxID, "Подтверждение точки устарело. Откройте /menu или отправьте геопозицию ещё раз.", nil)
	}
	if draft.Step == "done" && (draft.Version == version || draft.Version == version+1) {
		if state.Return.ParkingLocation != nil && state.Return.ParkingLocation.Source == "max_geo" && state.Return.ParkingLocation.Latitude == lat && state.Return.ParkingLocation.Longitude == lon {
			return p.sendView(ctx, maxID, "Точка парковки уже подтверждена. Продолжите возврат через /menu.", nil)
		}
		return p.sendView(ctx, maxID, "Точка парковки изменилась. Откройте /menu.", nil)
	}
	if draft.Step != "confirm" || draft.Version != version {
		return p.sendView(ctx, maxID, "Точка парковки изменилась. Откройте /menu.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return geo confirmation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "return.set_location")
	if err != nil {
		return err
	}
	reader, ok := p.Data.(ownCommandReader)
	if !ok {
		return errors.New("command result reader is not configured")
	}
	var saved dataapi.Return
	previous, err := reader.OwnCommandResult(ctx, actor, key, "return.set_location")
	if err == nil {
		saved, err = dataapi.DecodeAggregate[dataapi.Return](previous)
	} else {
		var apiErr *dataapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			return err
		}
		result, setErr := p.Commands.ReturnSetLocation(ctx, actor, returnID, state.Return.Version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}, dataapi.LocationInput{Latitude: lat, Longitude: lon, Source: "max_geo", Confirmed: true})
		if setErr != nil {
			if errors.As(setErr, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
				return p.sendView(ctx, maxID, "Место парковки не подтверждено: возврат изменился. Откройте /menu.", nil)
			}
			return setErr
		}
		saved, err = dataapi.DecodeAggregate[dataapi.Return](result)
	}
	if err != nil || saved.ID != returnID || saved.ParkingLocation == nil || saved.ParkingLocation.Source != "max_geo" || saved.ParkingLocation.Latitude != lat || saved.ParkingLocation.Longitude != lon {
		return errors.New("return geo confirmation returned invalid location")
	}
	context := draft.Context
	kind := "none"
	saveKey, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, dataapi.ConversationSaveInput{Flow: "return_location", Step: "done", PendingInputKind: &kind, Context: context}, saveKey, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		return err
	}
	completed, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || completed.Step != "done" || completed.Version != state.ConversationVersion+1 {
		return errors.New("return geo completion returned invalid conversation")
	}
	return p.sendView(ctx, maxID, "Точка парковки подтверждена. Продолжите возврат через /menu.", nil)
}
