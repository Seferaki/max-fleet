package dialog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

const adminCloseFlow = "trip_admin_close"

type adminCloseRequest struct {
	recognized       bool
	kind             string
	tripID           string
	tripVersion      int64
	challengeID      string
	challengeVersion int64
	option           int
	input            string
}

type adminCloseCommander interface {
	AdminChallengeCreate(context.Context, string, string, dataapi.AdminChallengeIntent, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	TripAdminClose(context.Context, string, string, int64, string, string, *dataapi.AdminCloseData, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
}

type adminCloseReturnReader interface {
	Return(context.Context, string, string) (dataapi.Return, error)
}

type adminCloseTarget struct {
	trip       dataapi.Trip
	vehicle    dataapi.Vehicle
	driver     dataapi.Employee
	inspection dataapi.Inspection
	parking    *dataapi.ParkingLocation
}

func parseAdminCloseEvent(event dataapi.NormalizedEvent) adminCloseRequest {
	if event.EventType == "message_created" && event.Payload.Kind == "text" && event.Payload.Text != nil {
		value := strings.TrimSpace(*event.Payload.Text)
		lower := strings.ToLower(value)
		if lower == "/adminclose" {
			return adminCloseRequest{recognized: true, kind: "menu"}
		}
		if strings.HasPrefix(lower, "/adminclose ") {
			return adminCloseRequest{recognized: true, kind: "input", input: strings.TrimSpace(value[len("/adminclose "):])}
		}
		return adminCloseRequest{}
	}
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return adminCloseRequest{}
	}
	value := *event.Payload.CallbackData
	if !strings.HasPrefix(value, "admin-close:") {
		return adminCloseRequest{}
	}
	parts := strings.Split(value, ":")
	request := adminCloseRequest{recognized: true, kind: "invalid"}
	if len(parts) == 4 && parts[0] == "admin-close" && (parts[1] == "start" || parts[1] == "resume" || parts[1] == "confirm" || parts[1] == "challenge" || parts[1] == "cancel") {
		request.kind, request.tripID = parts[1], parts[2]
		request.tripVersion = parseAdminIssueVersion(parts[3])
		return request
	}
	if len(parts) == 7 && parts[0] == "admin-close" && parts[1] == "answer" {
		request.kind, request.tripID, request.challengeID = "answer", parts[2], parts[4]
		request.tripVersion = parseAdminIssueVersion(parts[3])
		request.challengeVersion = parseAdminIssueVersion(parts[5])
		option, err := strconv.Atoi(parts[6])
		if err == nil && option >= 0 && option <= 3 {
			request.option = option
		} else {
			request.kind = "invalid"
		}
	}
	return request
}

func parseAdminCloseDetails(raw string) (string, *dataapi.AdminCloseData, string) {
	parts := strings.Split(raw, "|")
	reason := strings.TrimSpace(parts[0])
	if reason == "" || utf8.RuneCountInString(reason) > 1000 || strings.ContainsAny(reason, "\x00\r\n") {
		return "", nil, "Укажите причину до 1000 символов. Пример: /adminclose Водитель недоступен | fuel=50"
	}
	known := map[string]bool{}
	data := &dataapi.AdminCloseData{}
	for _, field := range parts[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if !ok || key == "" || value == "" || known[key] {
			return "", nil, "Формат сведений неверен: поле должно быть указано один раз как ключ=значение. Неизвестные значения пропустите."
		}
		known[key] = true
		switch key {
		case "fuel":
			if strings.EqualFold(value, "unknown") {
				continue
			}
			fuel, err := strconv.Atoi(value)
			if err != nil || fuel != 0 && fuel != 25 && fuel != 50 && fuel != 75 && fuel != 100 {
				return "", nil, "Топливо укажите как 0, 25, 50, 75, 100 или unknown."
			}
			data.FuelLevel = &fuel
		case "odo":
			if strings.EqualFold(value, "unknown") {
				continue
			}
			odometer, err := strconv.ParseInt(value, 10, 64)
			if err != nil || odometer < 0 || odometer > 10_000_000 {
				return "", nil, "Пробег укажите целым числом от 0 до 10000000 или unknown."
			}
			data.OdometerKM = &odometer
		case "location":
			if strings.EqualFold(value, "unknown") {
				continue
			}
			latitude, longitude, valid := returnDraftCoordinates(value)
			if !valid || math.Abs(longitude) > 180 {
				return "", nil, "Координаты укажите парой широта,долгота или unknown; например 55.75,37.61."
			}
			data.Latitude, data.Longitude = &latitude, &longitude
		case "landmark":
			if strings.EqualFold(value, "unknown") {
				continue
			}
			if utf8.RuneCountInString(value) > 200 || strings.ContainsAny(value, "\x00\r\n") {
				return "", nil, "Ориентир должен быть одной строкой не длиннее 200 символов."
			}
			copy := value
			data.Landmark = &copy
		case "keys", "locked":
			if strings.EqualFold(value, "unknown") {
				continue
			}
			var knownValue bool
			switch strings.ToLower(value) {
			case "yes":
				knownValue = true
			case "no":
				knownValue = false
			default:
				return "", nil, "Для keys и locked укажите yes, no или unknown."
			}
			if key == "keys" {
				data.KeysReturned = &knownValue
			} else {
				data.CarLocked = &knownValue
			}
		default:
			return "", nil, "Неизвестное поле «" + shortLabel(key) + "». Доступны fuel, odo, location, landmark, keys и locked."
		}
	}
	if data.Landmark != nil && data.Latitude == nil {
		return "", nil, "Ориентир можно указать только вместе с координатами location=широта,долгота."
	}
	return reason, data, ""
}

func (p Bootstrap) handleAdminCloseRequest(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, request adminCloseRequest) error {
	if employee.Role != "admin" {
		return p.sendView(ctx, maxID, "Административное закрытие поездки доступно только администратору автопарка.", nil)
	}
	switch request.kind {
	case "menu":
		if conversation := state.Conversation; adminCloseConversationActive(conversation) && conversation.Context.TripID != nil && conversation.Context.TripVersion != nil {
			return p.resumeAdminClose(ctx, item, actor, maxID, employee, state, *conversation.Context.TripID, *conversation.Context.TripVersion)
		}
		return p.sendView(ctx, maxID, "Для закрытия откройте активную поездку через /admintrips. В карточке выберите «Закрыть поездку администратором».", [][]maxsdk.Button{{{Text: "Поездки автопарка", Payload: "trip-list:admin:1"}}})
	case "start":
		return p.beginAdminClose(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion)
	case "resume":
		return p.resumeAdminClose(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion)
	case "input":
		return p.saveAdminCloseDetails(ctx, item, actor, maxID, employee, state, request.input)
	case "confirm":
		return p.createAdminCloseChallenge(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion)
	case "challenge":
		return p.renderAdminClose(ctx, actor, maxID, employee, state, request.tripID, request.tripVersion, "")
	case "answer":
		return p.answerAdminCloseChallenge(ctx, item, actor, maxID, employee, state, request)
	case "cancel":
		return p.cancelAdminClose(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion)
	default:
		return p.sendView(ctx, maxID, "Кнопка закрытия поездки повреждена или устарела. Откройте поездку через /admintrips.", [][]maxsdk.Button{{{Text: "Поездки автопарка", Payload: "trip-list:admin:1"}}})
	}
}

func (p Bootstrap) beginAdminClose(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64) error {
	if !vehicleIDPattern.MatchString(tripID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка поездки повреждена. Обновите /admintrips.", nil)
	}
	if conversation := state.Conversation; conversation != nil && conversation.Step != "done" && conversation.Step != "cancelled" {
		if conversation.Flow == adminCloseFlow && conversation.Context.TripID != nil && conversation.Context.TripVersion != nil && *conversation.Context.TripID == tripID && *conversation.Context.TripVersion == version {
			return p.resumeAdminClose(ctx, item, actor, maxID, employee, state, tripID, version)
		}
		payload := adminCloseResumePayload(conversation)
		if payload == "" {
			return p.sendView(ctx, maxID, "Сначала продолжите или завершите сохранённый диалог через /menu.", nil)
		}
		return p.sendView(ctx, maxID, "Сначала продолжите или завершите сохранённый диалог через /menu.", [][]maxsdk.Button{{{Text: "Продолжить диалог", Payload: payload}}})
	}
	target, current, err := p.loadAdminCloseTarget(ctx, actor, tripID, version)
	if err != nil {
		return p.adminCloseReadError(ctx, maxID, tripID, err)
	}
	if !current {
		return p.sendView(ctx, maxID, "Поездка изменилась или уже недоступна для admin close. Обновите /admintrips.", nil)
	}
	if item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin close requires a durable inbox lease")
	}
	pending := "text"
	input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "await_details", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{TripID: &tripID, TripVersion: &version}}
	if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminCloseSaveError(ctx, maxID, tripID, err)
	}
	return p.renderAdminCloseDetailsPrompt(ctx, maxID, target, tripID, version, "")
}

func (p Bootstrap) saveAdminCloseDetails(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, raw string) error {
	conversation := state.Conversation
	if !adminCloseAwaitingDetails(conversation) || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil {
		return p.sendView(ctx, maxID, "Сначала откройте поездку и начните admin close. Откройте /admintrips.", nil)
	}
	reason, available, parseError := parseAdminCloseDetails(raw)
	if parseError != "" {
		return p.sendView(ctx, maxID, parseError+"\n\nНеизвестные данные не указывайте.", [][]maxsdk.Button{{{Text: "Отменить закрытие", Payload: adminCloseActionPayload("cancel", *conversation.Context.TripID, *conversation.Context.TripVersion)}}})
	}
	tripID, version := *conversation.Context.TripID, *conversation.Context.TripVersion
	_, current, err := p.loadAdminCloseTarget(ctx, actor, tripID, version)
	if err != nil {
		return p.adminCloseReadError(ctx, maxID, tripID, err)
	}
	if !current {
		return p.sendView(ctx, maxID, "Поездка изменилась. Черновик не подтверждён; отмените его и откройте актуальную карточку.", [][]maxsdk.Button{{{Text: "Отменить черновик", Payload: adminCloseActionPayload("cancel", tripID, version)}}})
	}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "confirm", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{TripID: &tripID, TripVersion: &version, DraftText: &reason, AdminCloseData: available}}
	if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminCloseSaveError(ctx, maxID, tripID, err)
	}
	state.Conversation = &dataapi.Conversation{Flow: adminCloseFlow, Step: "confirm", Context: input.Context, PendingInputKind: &pending, Version: state.ConversationVersion + 1}
	state.ConversationVersion++
	return p.renderAdminClose(ctx, actor, maxID, employee, state, tripID, version, "")
}

func (p Bootstrap) resumeAdminClose(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminCloseFlow || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil ||
		!vehicleIDPattern.MatchString(tripID) || version < 1 || *conversation.Context.TripID != tripID || *conversation.Context.TripVersion != version {
		return p.sendView(ctx, maxID, "Сохранённое административное закрытие изменилось. Откройте поездку снова через /admintrips.", nil)
	}
	switch conversation.Step {
	case "done":
		return p.sendView(ctx, maxID, "Эта поездка уже закрыта администратором. Откройте /admintrips для проверки состояния машины.", [][]maxsdk.Button{{{Text: "Поездки автопарка", Payload: "trip-list:admin:1"}}})
	case "cancelled":
		return p.sendView(ctx, maxID, "Сохранённое закрытие отменено. При необходимости начните заново из актуальной карточки поездки.", nil)
	case "challenge":
		if _, ok := adminCloseDraft(state, tripID, version, "challenge"); !ok {
			return p.sendView(ctx, maxID, "Сохранённый math challenge неполон. Откройте /menu и проверьте шаг.", nil)
		}
		closed, result, err := p.recoverAdminCloseCommand(ctx, actor, tripID, version, valueOrEmpty(conversation.Context.ChallengeID))
		if err != nil {
			return err
		}
		if closed {
			return p.finishAdminClose(ctx, item, actor, maxID, employee, state, tripID, version, result, "Закрытие поездки было выполнено ранее и восстановлено.")
		}
	}
	return p.renderAdminClose(ctx, actor, maxID, employee, state, tripID, version, "")
}

func (p Bootstrap) createAdminCloseChallenge(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64) error {
	conversation, ok := adminCloseDraft(state, tripID, version, "confirm")
	if !ok {
		return p.sendView(ctx, maxID, "Сохранённое подтверждение неполно или изменилось. Откройте поездку через /admintrips.", nil)
	}
	_, current, err := p.loadAdminCloseTarget(ctx, actor, tripID, version)
	if err != nil {
		return p.adminCloseReadError(ctx, maxID, tripID, err)
	}
	if !current {
		return p.sendView(ctx, maxID, "Поездка изменилась. Старое подтверждение не будет применено.", [][]maxsdk.Button{{{Text: "Отменить закрытие", Payload: adminCloseActionPayload("cancel", tripID, version)}}})
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin close challenge requires a durable inbox lease")
	}
	commander, ok := p.Commands.(adminCloseCommander)
	if !ok {
		return errors.New("data-api admin close commands are not configured")
	}
	key, err := inboxworker.CommandKey(item, "challenge.create")
	if err != nil {
		return err
	}
	reason := *conversation.Context.DraftText
	intent := dataapi.AdminChallengeIntent{Operation: "trip.admin_close", TargetID: &tripID, ExpectedVersion: &version, Reason: &reason, AvailableData: conversation.Context.AdminCloseData}
	result, err := commander.AdminChallengeCreate(ctx, actor, "admin_close", intent, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
			return p.sendView(ctx, maxID, "Поездка изменилась. Старое подтверждение отменено; обновите /admintrips.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || result.Operation != "challenge.create" || !validAdminCloseChallenge(challenge) {
		return errors.New("admin_close challenge returned an invalid aggregate")
	}
	context := conversation.Context
	context.ChallengeID = &challenge.ID
	context.ChallengeVersion = &challenge.Version
	context.ChallengeQuestion = &challenge.Question
	context.ChallengeOptions = append([]int(nil), challenge.Options...)
	expires := challenge.ExpiresAt
	context.ChallengeExpiresAt = &expires
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "challenge", Context: context, PendingInputKind: &pending}
	if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminCloseSaveError(ctx, maxID, tripID, err)
	}
	return p.sendAdminCloseChallenge(ctx, maxID, tripID, version, challenge, "Подтвердите административное закрытие: выберите ответ. Неизвестные данные останутся незаполненными.")
}

func (p Bootstrap) answerAdminCloseChallenge(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, request adminCloseRequest) error {
	conversation, ok := adminCloseDraft(state, request.tripID, request.tripVersion, "challenge")
	if !ok {
		if state.Conversation != nil && state.Conversation.Flow == adminCloseFlow && state.Conversation.Step == "done" && state.Conversation.Context.TripID != nil && *state.Conversation.Context.TripID == request.tripID {
			return p.sendView(ctx, maxID, "Поездка уже закрыта администратором.", nil)
		}
		return p.sendView(ctx, maxID, "Вопрос или поездка изменились. Продолжите сохранённый шаг через /menu.", nil)
	}
	challengeID, challengeVersion := valueOrEmpty(conversation.Context.ChallengeID), valueOrZero(conversation.Context.ChallengeVersion)
	if challengeID != request.challengeID || challengeVersion != request.challengeVersion {
		return p.sendAdminCloseChallengeFromConversation(ctx, maxID, request.tripID, request.tripVersion, conversation, "Вопрос обновился. Выберите ответ из последнего сообщения.")
	}
	closed, closeResult, err := p.recoverAdminCloseCommand(ctx, actor, request.tripID, request.tripVersion, challengeID)
	if err != nil {
		return err
	}
	if closed {
		return p.finishAdminClose(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion, closeResult, "Закрытие поездки было выполнено ранее и восстановлено.")
	}
	_, current, err := p.loadAdminCloseTarget(ctx, actor, request.tripID, request.tripVersion)
	if err != nil {
		return p.adminCloseReadError(ctx, maxID, request.tripID, err)
	}
	if !current {
		return p.sendView(ctx, maxID, "Поездка изменилась. Вопрос больше не действует; обновите /admintrips.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin close challenge answer requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "challenge.answer")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeAnswer(ctx, actor, challengeID, challengeVersion, request.option, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "CHALLENGE_EXPIRED" {
			return p.returnAdminCloseToConfirm(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion, "Время вопроса истекло. Проверьте сведения и получите новый вопрос.")
		}
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
			return p.sendView(ctx, maxID, "Ответ устарел или вопрос недоступен. Откройте /menu и продолжите сохранённый шаг.", [][]maxsdk.Button{{{Text: "Продолжить закрытие", Payload: adminCloseActionPayload("resume", request.tripID, request.tripVersion)}}})
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || result.Operation != "challenge.answer" || !validAdminCloseChallenge(challenge) || challenge.ID != challengeID || challenge.Version != challengeVersion+1 || result.Correct == nil || result.AttemptsRemaining == nil || *result.AttemptsRemaining != challenge.AttemptsRemaining {
		return errors.New("admin_close challenge answer returned an invalid aggregate")
	}
	if !*result.Correct {
		if challenge.AttemptsRemaining == 0 {
			return p.returnAdminCloseToConfirm(ctx, item, actor, maxID, employee, state, request.tripID, request.tripVersion, "Три ответа неверны. Черновик сохранён; проверьте его и получите новый вопрос.")
		}
		context := adminCloseChallengeContext(conversation.Context, challenge)
		pending := "none"
		input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "challenge", Context: context, PendingInputKind: &pending}
		if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
			return p.adminCloseSaveError(ctx, maxID, request.tripID, err)
		}
		return p.sendAdminCloseChallenge(ctx, maxID, request.tripID, request.tripVersion, challenge, fmt.Sprintf("Ответ неверный. Осталось попыток: %d.", challenge.AttemptsRemaining))
	}
	if result.ChallengeProofID == nil || *result.ChallengeProofID != challenge.ID {
		return errors.New("admin_close challenge answer returned no matching proof")
	}
	return p.executeAdminClose(ctx, item, actor, maxID, employee, state, conversation, challenge.ID)
}

func (p Bootstrap) executeAdminClose(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, conversation *dataapi.Conversation, challengeID string) error {
	if conversation == nil || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil || conversation.Context.DraftText == nil || conversation.Context.AdminCloseData == nil || !vehicleIDPattern.MatchString(challengeID) {
		return p.sendView(ctx, maxID, "Сохранённый admin close неполон. Откройте /admintrips.", nil)
	}
	tripID, version := *conversation.Context.TripID, *conversation.Context.TripVersion
	closed, result, err := p.recoverAdminCloseCommand(ctx, actor, tripID, version, challengeID)
	if err != nil {
		return err
	}
	if !closed {
		_, current, err := p.loadAdminCloseTarget(ctx, actor, tripID, version)
		if err != nil {
			return p.adminCloseReadError(ctx, maxID, tripID, err)
		}
		if !current {
			return p.sendView(ctx, maxID, "Поездка изменилась. Административное закрытие не применено.", nil)
		}
		if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
			return errors.New("admin close requires a durable inbox lease")
		}
		commander, ok := p.Commands.(adminCloseCommander)
		if !ok {
			return errors.New("data-api admin close commands are not configured")
		}
		key := adminCloseCommandKey(challengeID)
		lease := &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}
		result, err = commander.TripAdminClose(ctx, actor, tripID, version, *conversation.Context.DraftText, challengeID, conversation.Context.AdminCloseData, key, lease)
		if err != nil {
			var apiErr *dataapi.APIError
			if errors.As(err, &apiErr) && apiErr.Code == "CHALLENGE_EXPIRED" {
				return p.returnAdminCloseToConfirm(ctx, item, actor, maxID, employee, state, tripID, version, "Подтверждение истекло. Проверьте сведения и получите новый вопрос.")
			}
			if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
				return p.sendView(ctx, maxID, "Поездка изменилась или подтверждение уже использовано. Обновите /admintrips.", nil)
			}
			if errors.As(err, &apiErr) && apiErr.Status == 403 {
				return p.sendView(ctx, maxID, "Подтверждение недействительно. Закрытие не выполнено; проверьте роль и начните новый admin close.", nil)
			}
			return err
		}
	}
	return p.finishAdminClose(ctx, item, actor, maxID, employee, state, tripID, version, result, "")
}

func (p Bootstrap) recoverAdminCloseCommand(ctx context.Context, actor, tripID string, version int64, challengeID string) (bool, dataapi.CommandResult, error) {
	reader, ok := p.Data.(ownCommandReader)
	if !ok {
		return false, dataapi.CommandResult{}, errors.New("admin close command recovery reader is not configured")
	}
	if !vehicleIDPattern.MatchString(challengeID) {
		return false, dataapi.CommandResult{}, errors.New("admin close challenge id is invalid")
	}
	result, err := reader.OwnCommandResult(ctx, actor, adminCloseCommandKey(challengeID), "trip.admin_close")
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return false, dataapi.CommandResult{}, nil
		}
		return false, dataapi.CommandResult{}, err
	}
	trip, err := dataapi.DecodeAggregate[dataapi.Trip](result)
	if err != nil || result.Operation != "trip.admin_close" || trip.ID != tripID || trip.Version != version+1 || trip.Status != "closed_by_admin" {
		return false, dataapi.CommandResult{}, errors.New("saved trip.admin_close result does not match the requested trip")
	}
	return true, result, nil
}

func (p Bootstrap) finishAdminClose(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, result dataapi.CommandResult, lead string) error {
	trip, err := dataapi.DecodeAggregate[dataapi.Trip](result)
	if err != nil || result.Operation != "trip.admin_close" || trip.ID != tripID || trip.Version != version+1 || trip.Status != "closed_by_admin" {
		return errors.New("trip.admin_close returned an invalid aggregate")
	}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "done", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{TripID: &tripID, TripVersion: &version}}
	if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminCloseSaveError(ctx, maxID, tripID, err)
	}
	message := "Поездка безопасно закрыта администратором. Машина отправлена на проверку и остаётся недоступной."
	if len(trip.MissingData) > 0 {
		message += " Не хватает: " + adminCloseMissingText(trip.MissingData) + "."
	}
	if lead != "" {
		message = lead + "\n" + message
	}
	return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "Поездки автопарка", Payload: "trip-list:admin:1"}}})
}

func (p Bootstrap) cancelAdminClose(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminCloseFlow || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil ||
		*conversation.Context.TripID != tripID || *conversation.Context.TripVersion != version || !adminCloseConversationActive(conversation) {
		return p.sendView(ctx, maxID, "Сохранённый admin close уже изменился. Откройте /admintrips.", nil)
	}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "cancelled", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{TripID: &tripID, TripVersion: &version}}
	if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminCloseSaveError(ctx, maxID, tripID, err)
	}
	return p.sendView(ctx, maxID, "Admin close отменён. Состояние поездки и автомобиля не менялось.", nil)
}

func (p Bootstrap) returnAdminCloseToConfirm(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, lead string) error {
	conversation, ok := adminCloseDraft(state, tripID, version, "challenge")
	if !ok {
		return p.sendView(ctx, maxID, "Вопрос изменился. Откройте /menu и проверьте сохранённый шаг.", nil)
	}
	context := dataapi.ConversationContext{TripID: &tripID, TripVersion: &version, DraftText: conversation.Context.DraftText, AdminCloseData: conversation.Context.AdminCloseData}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminCloseFlow, Step: "confirm", Context: context, PendingInputKind: &pending}
	if _, err := p.saveAdminCloseConversation(ctx, item, actor, employee, state, input); err != nil {
		return p.adminCloseSaveError(ctx, maxID, tripID, err)
	}
	state.ConversationVersion++
	state.Conversation = &dataapi.Conversation{Flow: adminCloseFlow, Step: "confirm", Context: context, PendingInputKind: &pending, Version: state.ConversationVersion}
	return p.renderAdminClose(ctx, actor, maxID, employee, state, tripID, version, lead)
}

func (p Bootstrap) renderAdminClose(ctx context.Context, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, tripID string, version int64, lead string) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminCloseFlow || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil ||
		*conversation.Context.TripID != tripID || *conversation.Context.TripVersion != version {
		return p.sendView(ctx, maxID, "Сохранённое административное закрытие не найдено. Откройте /admintrips.", nil)
	}
	if conversation.Step == "done" || conversation.Step == "cancelled" {
		return p.resumeAdminClose(ctx, dataapi.InboxClaimItem{}, actor, maxID, employee, state, tripID, version)
	}
	target, current, err := p.loadAdminCloseTarget(ctx, actor, tripID, version)
	if err != nil {
		return p.adminCloseReadError(ctx, maxID, tripID, err)
	}
	if !current {
		return p.sendView(ctx, maxID, "Поездка изменилась; старое подтверждение использовать нельзя. Обновите /admintrips или отмените черновик.", [][]maxsdk.Button{{{Text: "Отменить черновик", Payload: adminCloseActionPayload("cancel", tripID, version)}}})
	}
	switch conversation.Step {
	case "await_details":
		if conversation.Context.DraftText != nil || conversation.Context.AdminCloseData != nil || conversation.PendingInputKind == nil || *conversation.PendingInputKind != "text" {
			return p.sendView(ctx, maxID, "Сохранённый шаг неполон. Отмените черновик и начните заново.", nil)
		}
		return p.renderAdminCloseDetailsPrompt(ctx, maxID, target, tripID, version, lead)
	case "confirm":
		if _, ok := adminCloseDraft(state, tripID, version, "confirm"); !ok {
			return p.sendView(ctx, maxID, "Сохранённое подтверждение неполно. Отмените его и начните заново.", nil)
		}
		return p.renderAdminCloseConfirmation(ctx, maxID, target, conversation, lead)
	case "challenge":
		if _, ok := adminCloseDraft(state, tripID, version, "challenge"); !ok {
			return p.sendView(ctx, maxID, "Сохранённый вопрос неполон. Откройте /menu и проверьте шаг.", nil)
		}
		return p.sendAdminCloseChallengeFromConversation(ctx, maxID, tripID, version, conversation, lead)
	default:
		return p.sendView(ctx, maxID, "Шаг admin close неизвестен. Отмените черновик и начните заново.", [][]maxsdk.Button{{{Text: "Отменить черновик", Payload: adminCloseActionPayload("cancel", tripID, version)}}})
	}
}

func (p Bootstrap) renderAdminCloseDetailsPrompt(ctx context.Context, maxID int64, target adminCloseTarget, tripID string, version int64, lead string) error {
	text := fmt.Sprintf("Закрытие поездки администратором\nПоездка: %s · %s\nАвтомобиль: %s\nСотрудник: %s\nВерсия поездки: %d\n\nОтправьте причину и только достоверные сведения:\n/adminclose <причина> | fuel=50 | odo=42150 | location=55.75,37.61 | landmark=У входа | keys=yes | locked=no\nПоля необязательны; unknown или пропуск оставляет поле неизвестным. Фото этим действием не добавляются.", shortLabel(tripID), oneLine(target.trip.Status), oneLine(target.vehicle.Plate), adminCloseEmployeeLabel(target.driver), version)
	if lead != "" {
		text = lead + "\n" + text
	}
	return p.sendView(ctx, maxID, text, [][]maxsdk.Button{{{Text: "Отменить закрытие", Payload: adminCloseActionPayload("cancel", tripID, version)}}})
}

func (p Bootstrap) renderAdminCloseConfirmation(ctx context.Context, maxID int64, target adminCloseTarget, conversation *dataapi.Conversation, lead string) error {
	tripID, version := *conversation.Context.TripID, *conversation.Context.TripVersion
	data := conversation.Context.AdminCloseData
	inspection := target.inspection
	if data.FuelLevel != nil {
		inspection.FuelLevel = data.FuelLevel
	}
	if data.OdometerKM != nil {
		inspection.OdometerKM = data.OdometerKM
	}
	if data.KeysReturned != nil {
		inspection.KeysReturned = data.KeysReturned
	}
	if data.CarLocked != nil {
		inspection.CarLocked = data.CarLocked
	}
	parking := target.parking
	if data.Latitude != nil && data.Longitude != nil {
		parking = &dataapi.ParkingLocation{Latitude: *data.Latitude, Longitude: *data.Longitude, Landmark: data.Landmark}
	}
	missing := adminCloseMissingFields(inspection, parking)
	known := adminCloseDataSummary(data)
	if known == "" {
		known = "дополнительных сведений нет"
	}
	missingText := "все перечисленные сведения известны"
	if len(missing) > 0 {
		missingText = adminCloseMissingText(missing)
	}
	text := fmt.Sprintf("Подтвердите admin close\nПоездка: %s · %s\nАвтомобиль: %s\nСотрудник: %s\nПричина: %s\nДополнительно явно известно: %s\n\nПосле действия не хватает: %s. Неизвестные значения не будут подменены нулями или ответами «нет». Машина останется недоступной до проверки.", shortLabel(tripID), oneLine(target.trip.Status), oneLine(target.vehicle.Plate), adminCloseEmployeeLabel(target.driver), shortLabel(oneLine(*conversation.Context.DraftText)), known, missingText)
	if lead != "" {
		text = lead + "\n" + text
	}
	rows := [][]maxsdk.Button{{{Text: "Проверить и закрыть", Payload: adminCloseActionPayload("confirm", tripID, version)}}, {{Text: "Отменить", Payload: adminCloseActionPayload("cancel", tripID, version)}}}
	return p.sendView(ctx, maxID, text, rows)
}

func (p Bootstrap) sendAdminCloseChallenge(ctx context.Context, maxID int64, tripID string, tripVersion int64, challenge dataapi.Challenge, lead string) error {
	rows := make([][]maxsdk.Button, 0, 4)
	for index, value := range challenge.Options {
		payload := fmt.Sprintf("admin-close:answer:%s:%d:%s:%d:%d", tripID, tripVersion, challenge.ID, challenge.Version, index)
		rows = append(rows, []maxsdk.Button{{Text: strconv.Itoa(value), Payload: payload}})
	}
	text := lead + "\n" + oneLine(challenge.Question) + "\nВопрос до: " + formatMoment(challenge.ExpiresAt, p.Location)
	return p.sendView(ctx, maxID, text, rows)
}

func (p Bootstrap) sendAdminCloseChallengeFromConversation(ctx context.Context, maxID int64, tripID string, tripVersion int64, conversation *dataapi.Conversation, lead string) error {
	context := conversation.Context
	if context.ChallengeID == nil || context.ChallengeVersion == nil || context.ChallengeQuestion == nil || context.ChallengeExpiresAt == nil || len(context.ChallengeOptions) != 4 {
		return p.sendView(ctx, maxID, "Сохранённый math challenge неполон. Откройте /menu.", nil)
	}
	challenge := dataapi.Challenge{ID: *context.ChallengeID, Purpose: "admin_close", Version: *context.ChallengeVersion, Question: *context.ChallengeQuestion, Options: append([]int(nil), context.ChallengeOptions...), ExpiresAt: *context.ChallengeExpiresAt}
	if !validAdminCloseChallenge(challenge) {
		return p.sendView(ctx, maxID, "Сохранённый math challenge повреждён. Откройте /menu.", nil)
	}
	if lead == "" {
		lead = "Продолжите сохранённый math challenge."
	}
	return p.sendAdminCloseChallenge(ctx, maxID, tripID, tripVersion, challenge, lead)
}

func (p Bootstrap) loadAdminCloseTarget(ctx context.Context, actor, tripID string, version int64) (adminCloseTarget, bool, error) {
	reader, ok := p.Data.(tripPhotoReader)
	if !ok {
		return adminCloseTarget{}, false, errors.New("admin close trip reader is not configured")
	}
	trip, err := reader.Trip(ctx, actor, tripID)
	if err != nil {
		return adminCloseTarget{}, false, err
	}
	if trip.ID != tripID || trip.Version < 1 {
		return adminCloseTarget{}, false, errors.New("data-api returned an invalid admin close trip")
	}
	target := adminCloseTarget{trip: trip, inspection: dataapi.Inspection{Phase: "after", Status: "draft"}, parking: trip.ParkingLocation}
	if trip.AfterInspection != nil {
		target.inspection = *trip.AfterInspection
	}
	if trip.Version != version || trip.Status != "active" && trip.Status != "returning" {
		return target, false, nil
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, trip.VehicleID)
	if err != nil {
		return adminCloseTarget{}, false, err
	}
	driverReader, ok := p.Data.(adminIssueEmployeeReader)
	if !ok {
		return adminCloseTarget{}, false, errors.New("admin close employee reader is not configured")
	}
	driver, err := driverReader.AdminEmployee(ctx, actor, trip.EmployeeID)
	if err != nil {
		return adminCloseTarget{}, false, err
	}
	if vehicle.ID != trip.VehicleID || driver.ID != trip.EmployeeID || driver.Role == "admin" {
		return adminCloseTarget{}, false, errors.New("data-api returned an invalid admin close target projection")
	}
	target.vehicle, target.driver = vehicle, driver
	if trip.Status == "returning" {
		if trip.ReturnID == nil {
			return adminCloseTarget{}, false, errors.New("returning trip has no current return projection")
		}
		returnReader, ok := p.Data.(adminCloseReturnReader)
		if !ok {
			return adminCloseTarget{}, false, errors.New("admin close return reader is not configured")
		}
		currentReturn, err := returnReader.Return(ctx, actor, *trip.ReturnID)
		if err != nil {
			return adminCloseTarget{}, false, err
		}
		if currentReturn.ID != *trip.ReturnID || currentReturn.TripID != trip.ID || currentReturn.Status != "draft" || currentReturn.Inspection.Phase != "after" {
			return adminCloseTarget{}, false, errors.New("data-api returned an invalid admin close return projection")
		}
		target.inspection, target.parking = currentReturn.Inspection, currentReturn.ParkingLocation
	}
	return target, true, nil
}

func (p Bootstrap) saveAdminCloseConversation(ctx context.Context, item dataapi.InboxClaimItem, actor string, employee dataapi.Employee, state dataapi.CurrentState, input dataapi.ConversationSaveInput) (dataapi.Conversation, error) {
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" || input.Context.TripID == nil || input.Context.TripVersion == nil {
		return dataapi.Conversation{}, errors.New("admin close conversation save requires a durable inbox lease and trip")
	}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return dataapi.Conversation{}, err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, input, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		return dataapi.Conversation{}, err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || result.Operation != "conversation.save" || saved.Flow != adminCloseFlow || saved.Step != input.Step || saved.Version != state.ConversationVersion+1 ||
		!reflect.DeepEqual(saved.Context, input.Context) || !sameOptionalString(saved.PendingInputKind, input.PendingInputKind) {
		return dataapi.Conversation{}, errors.New("conversation.save returned an invalid admin close state")
	}
	return saved, nil
}

func (p Bootstrap) adminCloseSaveError(ctx context.Context, maxID int64, tripID string, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
		return p.sendView(ctx, maxID, "Поездка или сохранённый admin close изменились. Откройте /admintrips и проверьте текущую версию.", [][]maxsdk.Button{{{Text: "Поездки автопарка", Payload: "trip-list:admin:1"}}})
	}
	return err
}

func (p Bootstrap) adminCloseReadError(ctx context.Context, maxID int64, tripID string, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return p.sendView(ctx, maxID, "Поездка больше недоступна. Откройте /admintrips и обновите список.", nil)
	}
	if errors.As(err, &apiErr) && apiErr.Status == 403 {
		return p.sendView(ctx, maxID, "Доступ к административному закрытию отозван. Проверьте текущую роль администратора.", nil)
	}
	return err
}

func adminCloseConversationActive(conversation *dataapi.Conversation) bool {
	return conversation != nil && conversation.Flow == adminCloseFlow && conversation.Step != "done" && conversation.Step != "cancelled"
}

func adminCloseAwaitingDetails(conversation *dataapi.Conversation) bool {
	return adminCloseConversationActive(conversation) && conversation.Step == "await_details"
}

func adminCloseDraft(state dataapi.CurrentState, tripID string, version int64, step string) (*dataapi.Conversation, bool) {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminCloseFlow || conversation.Step != step || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil ||
		*conversation.Context.TripID != tripID || *conversation.Context.TripVersion != version || conversation.PendingInputKind == nil || *conversation.PendingInputKind != "none" {
		return nil, false
	}
	context := conversation.Context
	if !vehicleIDPattern.MatchString(tripID) || version < 1 || context.DraftText == nil || strings.TrimSpace(*context.DraftText) == "" || context.AdminCloseData == nil {
		return nil, false
	}
	if step == "confirm" {
		if context.ChallengeID != nil || context.ChallengeVersion != nil || context.ChallengeQuestion != nil || len(context.ChallengeOptions) != 0 || context.ChallengeExpiresAt != nil {
			return nil, false
		}
	} else if step == "challenge" {
		if context.ChallengeID == nil || context.ChallengeVersion == nil || context.ChallengeQuestion == nil || strings.TrimSpace(*context.ChallengeQuestion) == "" || len(context.ChallengeOptions) != 4 || context.ChallengeExpiresAt == nil {
			return nil, false
		}
	}
	return conversation, true
}

func validAdminCloseChallenge(challenge dataapi.Challenge) bool {
	if !vehicleIDPattern.MatchString(challenge.ID) || challenge.Purpose != "admin_close" || strings.TrimSpace(challenge.Question) == "" || len(challenge.Options) != 4 || challenge.Version < 1 || challenge.AttemptsRemaining < 0 || challenge.AttemptsRemaining > 3 || challenge.ExpiresAt.IsZero() {
		return false
	}
	seen := map[int]bool{}
	for _, option := range challenge.Options {
		if option < 0 || seen[option] {
			return false
		}
		seen[option] = true
	}
	return true
}

func adminCloseChallengeContext(context dataapi.ConversationContext, challenge dataapi.Challenge) dataapi.ConversationContext {
	context.ChallengeID = &challenge.ID
	context.ChallengeVersion = &challenge.Version
	context.ChallengeQuestion = &challenge.Question
	context.ChallengeOptions = append([]int(nil), challenge.Options...)
	expires := challenge.ExpiresAt
	context.ChallengeExpiresAt = &expires
	return context
}

func adminCloseCommandKey(challengeID string) string { return "admin-close-command-" + challengeID }

func adminCloseActionPayload(action, tripID string, version int64) string {
	return fmt.Sprintf("admin-close:%s:%s:%d", action, tripID, version)
}

func adminCloseResumePayload(conversation *dataapi.Conversation) string {
	if conversation == nil || conversation.Context.TripID == nil || conversation.Context.TripVersion == nil {
		return ""
	}
	return adminCloseActionPayload("resume", *conversation.Context.TripID, *conversation.Context.TripVersion)
}

func adminCloseEmployeeLabel(employee dataapi.Employee) string {
	if label := oneLine(employee.DisplayName); label != "" {
		return label
	}
	return "Сотрудник"
}

func adminCloseDataSummary(data *dataapi.AdminCloseData) string {
	if data == nil {
		return ""
	}
	values := []string{}
	if data.FuelLevel != nil {
		values = append(values, fmt.Sprintf("топливо %d%%", *data.FuelLevel))
	}
	if data.OdometerKM != nil {
		values = append(values, fmt.Sprintf("пробег %d км", *data.OdometerKM))
	}
	if data.Latitude != nil && data.Longitude != nil {
		location := fmt.Sprintf("место %.6f, %.6f", *data.Latitude, *data.Longitude)
		if data.Landmark != nil {
			location += " (" + oneLine(*data.Landmark) + ")"
		}
		values = append(values, location)
	}
	if data.KeysReturned != nil {
		values = append(values, "ключи "+yesNo(*data.KeysReturned))
	}
	if data.CarLocked != nil {
		values = append(values, "машина "+adminCloseLockedLabel(*data.CarLocked))
	}
	return strings.Join(values, "; ")
}

func adminCloseMissingFields(inspection dataapi.Inspection, parking *dataapi.ParkingLocation) []string {
	missing := make([]string, 0, 6)
	if len(inspection.OccupiedSlots) < 8 {
		missing = append(missing, "after_photos")
	}
	if inspection.FuelLevel == nil {
		missing = append(missing, "fuel_level")
	}
	if inspection.OdometerKM == nil {
		missing = append(missing, "odometer_km")
	}
	if parking == nil {
		missing = append(missing, "parking_location")
	}
	if inspection.KeysReturned == nil {
		missing = append(missing, "keys_returned")
	}
	if inspection.CarLocked == nil {
		missing = append(missing, "car_locked")
	}
	return missing
}

func adminCloseMissingText(fields []string) string {
	labels := map[string]string{"after_photos": "фото после", "fuel_level": "топливо", "odometer_km": "пробег", "parking_location": "место парковки", "keys_returned": "возврат ключей", "car_locked": "закрытие машины"}
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		if label, ok := labels[field]; ok {
			values = append(values, label)
		} else {
			values = append(values, shortLabel(field))
		}
	}
	return strings.Join(values, ", ")
}

func adminCloseLockedLabel(locked bool) string {
	if locked {
		return "закрыта"
	}
	return "не закрыта"
}
