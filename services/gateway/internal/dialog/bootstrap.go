package dialog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type Reader interface {
	Me(context.Context, string) (dataapi.Me, error)
	State(context.Context, string) (dataapi.CurrentState, error)
	CurrentRules(context.Context, string) (dataapi.Rules, error)
	Vehicles(context.Context, string, dataapi.VehicleFilter) (dataapi.Page[dataapi.Vehicle], error)
	Vehicle(context.Context, string, string) (dataapi.Vehicle, error)
	PreviousInspection(context.Context, string, string) (dataapi.Inspection, error)
}

type CheckoutCommander interface {
	CheckoutCreate(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	CheckoutCancel(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ChallengeCreateTake(context.Context, string, string, int64, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ChallengeAnswer(context.Context, string, string, int64, int, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	CheckoutAcceptRules(context.Context, string, string, int64, string, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
}

// Bootstrap handles implemented menu, catalog and checkout entry events. Other
// accepted events remain durable and unacknowledged until their flow exists.
type Bootstrap struct {
	Data     Reader
	Commands CheckoutCommander
	MAX      maxsdk.Transport
	Location *time.Location
}

var vehicleIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (p Bootstrap) Handle(ctx context.Context, item dataapi.InboxClaimItem) error {
	pageNumber, catalog := catalogPage(item.Event)
	vehicleID, expectedVersion, card := cardTarget(item.Event)
	previousVehicleID, previous := previousTarget(item.Event)
	actionVehicleID, actionVersion, intent := vehicleActionTarget(item.Event, "intent:")
	confirmVehicleID, confirmVersion, confirm := vehicleActionTarget(item.Event, "take:")
	cancelID, cancelVersion, cancelIntent := vehicleActionTarget(item.Event, "cancel-intent:")
	confirmedCancelID, confirmedCancelVersion, confirmedCancel := vehicleActionTarget(item.Event, "cancel:")
	mathCheckoutID, mathVersion, math := vehicleActionTarget(item.Event, "math:")
	challengeID, challengeVersion, selectedOption, answer := answerTarget(item.Event)
	rulesID, rulesVersion, rules := vehicleActionTarget(item.Event, "rules:")
	acceptCheckoutID, acceptVersion, acceptedRulesID, acceptRules := acceptRulesTarget(item.Event)
	photoCheckoutID, photoVersion, photos := vehicleActionTarget(item.Event, "photos:")
	if !catalog && !card && !previous && !intent && !confirm && !cancelIntent && !confirmedCancel && !math && !answer && !rules && !acceptRules && !photos && !isMenuEvent(item.Event) {
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
	if intent || confirm {
		vehicleID, version := actionVehicleID, actionVersion
		if confirm {
			vehicleID, version = confirmVehicleID, confirmVersion
		}
		return p.checkoutIntent(ctx, item, actor, maxID, *me.Employee, state, vehicleID, version, confirm)
	}
	if cancelIntent || confirmedCancel {
		checkoutID, version := cancelID, cancelVersion
		if confirmedCancel {
			checkoutID, version = confirmedCancelID, confirmedCancelVersion
		}
		return p.cancelCheckout(ctx, item, actor, maxID, state, checkoutID, version, confirmedCancel)
	}
	if math {
		return p.createMath(ctx, item, actor, maxID, state, mathCheckoutID, mathVersion)
	}
	if answer {
		return p.answerMath(ctx, item, actor, maxID, state, challengeID, challengeVersion, selectedOption)
	}
	if rules || acceptRules {
		checkoutID, version := rulesID, rulesVersion
		if acceptRules {
			checkoutID, version = acceptCheckoutID, acceptVersion
		}
		return p.checkoutRules(ctx, item, actor, maxID, state, checkoutID, version, acceptedRulesID, acceptRules)
	}
	if photos {
		return p.checkoutPhotos(ctx, maxID, state, photoCheckoutID, photoVersion)
	}
	if catalog {
		if pageNumber == 0 {
			return p.sendView(ctx, maxID, "Кнопка списка устарела или повреждена. Обновите список.", [][]maxsdk.Button{{{Text: "Обновить", Payload: "cars:1"}}})
		}
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
				message += "\n" + checkoutAvailabilityText(vehicle, *me.Employee, state)
				rows = append([][]maxsdk.Button{{{Text: "Предыдущий осмотр", Payload: "prev:" + vehicle.ID}}}, rows...)
				if vehicle.CurrentParking != nil {
					rows = append([][]maxsdk.Button{{{Text: "Показать на карте", URL: parkingMapURL(*vehicle.CurrentParking)}}}, rows...)
				}
				if canOfferCheckout(vehicle, *me.Employee, state) {
					rows = append([][]maxsdk.Button{{{Text: "Начать оформление", Payload: fmt.Sprintf("intent:%s:%d", vehicle.ID, vehicle.Version)}}}, rows...)
				}
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
	message := menuText(*me.Employee, state)
	if state.Checkout != nil {
		message += "\nHold до: " + formatMoment(state.Checkout.ExpiresAt, p.Location)
		if state.Checkout.Status == "holding" && state.Checkout.Step == "inspection" {
			message += "\n" + photoProgress(state.Checkout.Inspection)
		}
	}
	return p.sendView(ctx, maxID, message, menuRows(*me.Employee, state))
}

func answerTarget(event dataapi.NormalizedEvent) (string, int64, int, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "answer:") {
		return "", 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "answer:"), ":")
	if len(parts) != 3 {
		return "", 0, 0, true
	}
	version, versionErr := strconv.ParseInt(parts[1], 10, 64)
	option, optionErr := strconv.Atoi(parts[2])
	if versionErr != nil || optionErr != nil || version < 1 || option < 0 || option > 3 {
		return "", 0, 0, true
	}
	return parts[0], version, option, true
}

func acceptRulesTarget(event dataapi.NormalizedEvent) (string, int64, string, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "accept-rules:") {
		return "", 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "accept-rules:"), ":")
	if len(parts) != 3 {
		return "", 0, "", true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, "", true
	}
	return parts[0], version, parts[2], true
}

func (p Bootstrap) createMath(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, checkoutID string, version int64) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка вопроса повреждена. Откройте /menu.", nil)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID || checkout.Status != "holding" || checkout.Step != "math" || checkout.Version != version {
		return p.sendView(ctx, maxID, "Шаг оформления изменился или hold истёк. Обновите /menu.", nil)
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, checkout.VehicleID)
	if err != nil {
		return err
	}
	if vehicle.Version < 2 || p.Commands == nil || item.LeaseToken == "" {
		return errors.New("math challenge requires a durable inbox lease and vehicle version")
	}
	key, err := inboxworker.CommandKey(item, "challenge.create")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeCreateTake(ctx, actor, checkout.ID, checkout.Version, checkout.VehicleID, vehicle.Version-1, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 {
			return p.sendView(ctx, maxID, "Вопрос устарел или hold истёк. Обновите /menu.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || !validTakeChallenge(challenge) {
		return errors.New("challenge create returned invalid aggregate")
	}
	return p.sendChallenge(ctx, maxID, challenge, "Выберите ответ. Попыток: 3.")
}

func (p Bootstrap) answerMath(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, challengeID string, version int64, option int) error {
	if !vehicleIDPattern.MatchString(challengeID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка ответа повреждена. Откройте /menu.", nil)
	}
	if state.Checkout == nil || state.Checkout.Status != "holding" || state.Checkout.Step != "math" {
		return p.sendView(ctx, maxID, "Шаг подтверждения изменился или hold истёк. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("challenge answer requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "challenge.answer")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeAnswer(ctx, actor, challengeID, version, option, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Ответ уже устарел или вопрос недоступен. Начните новый через /menu.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || !validTakeChallenge(challenge) || challenge.ID != challengeID || result.Correct == nil || result.AttemptsRemaining == nil || *result.AttemptsRemaining != challenge.AttemptsRemaining {
		return errors.New("challenge answer returned invalid aggregate")
	}
	if *result.Correct {
		return p.sendView(ctx, maxID, "Ответ верный. Следующий шаг — правила. Откройте /menu.", nil)
	}
	if *result.AttemptsRemaining == 0 {
		return p.sendView(ctx, maxID, "Три неверных ответа. Получите новый вопрос через /menu, пока hold действует.", nil)
	}
	return p.sendChallenge(ctx, maxID, challenge, fmt.Sprintf("Ответ неверный. Осталось попыток: %d.", *result.AttemptsRemaining))
}

func validTakeChallenge(challenge dataapi.Challenge) bool {
	return vehicleIDPattern.MatchString(challenge.ID) && challenge.Purpose == "take" && challenge.Question != "" && len(challenge.Options) == 4 && challenge.Version > 0 && challenge.AttemptsRemaining >= 0 && challenge.AttemptsRemaining <= 3 && !challenge.ExpiresAt.IsZero()
}

func (p Bootstrap) sendChallenge(ctx context.Context, maxID int64, challenge dataapi.Challenge, lead string) error {
	rows := make([][]maxsdk.Button, 0, 4)
	for index, value := range challenge.Options {
		rows = append(rows, []maxsdk.Button{{Text: strconv.Itoa(value), Payload: fmt.Sprintf("answer:%s:%d:%d", challenge.ID, challenge.Version, index)}})
	}
	text := lead + "\n" + oneLine(challenge.Question) + "\nВопрос до: " + formatMoment(challenge.ExpiresAt, p.Location)
	return p.sendView(ctx, maxID, text, rows)
}

func (p Bootstrap) checkoutRules(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, checkoutID string, version int64, shownRulesID string, accept bool) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 || accept && !vehicleIDPattern.MatchString(shownRulesID) {
		return p.sendView(ctx, maxID, "Кнопка правил повреждена. Откройте /menu.", nil)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID || checkout.Version != version || checkout.Status != "holding" || checkout.Step != "rules" || checkout.IntentConfirmedAt == nil || checkout.RulesAcceptedAt != nil {
		return p.sendView(ctx, maxID, "Шаг правил изменился или hold истёк. Обновите /menu.", nil)
	}
	rules, err := p.Data.CurrentRules(ctx, actor)
	if err != nil {
		return err
	}
	if !vehicleIDPattern.MatchString(rules.ID) || strings.TrimSpace(rules.Body) == "" || strings.TrimSpace(rules.VersionLabel) == "" || utf8.RuneCountInString(rules.Body) > 10000 || utf8.RuneCountInString(rules.VersionLabel) > 50 {
		return errors.New("current rules are invalid")
	}
	if !accept {
		return p.sendRules(ctx, maxID, *checkout, rules, "")
	}
	if shownRulesID != rules.ID {
		return p.sendRules(ctx, maxID, *checkout, rules, "Правила изменились. Прочитайте текущую версию.\n")
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("rules acceptance requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.accept_rules")
	if err != nil {
		return err
	}
	result, err := p.Commands.CheckoutAcceptRules(ctx, actor, checkout.ID, checkout.Version, rules.ID, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Правила или оформление изменились. Обновите /menu и прочитайте текущую версию.", nil)
		}
		return err
	}
	accepted, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || accepted.ID != checkout.ID || accepted.Status != "holding" || accepted.Step != "inspection" || accepted.RulesVersionID == nil || *accepted.RulesVersionID != rules.ID || accepted.RulesAcceptedAt == nil {
		return errors.New("rules acceptance returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Правила версии "+oneLine(rules.VersionLabel)+" приняты. Осмотр до поездки: нужно 8 фотографий. Откройте /menu для продолжения.", nil)
}

func (p Bootstrap) sendRules(ctx context.Context, maxID int64, checkout dataapi.Checkout, rules dataapi.Rules, lead string) error {
	const chunkSize = 3500 // Leaves space for version and part labels under MAX's 4000-rune limit.
	body := []rune(rules.Body)
	parts := (len(body) + chunkSize - 1) / chunkSize
	rows := [][]maxsdk.Button{{{Text: "Принимаю правила", Payload: fmt.Sprintf("accept-rules:%s:%d:%s", checkout.ID, checkout.Version, rules.ID)}}, {{Text: "Отменить оформление", Payload: fmt.Sprintf("cancel-intent:%s:%d", checkout.ID, checkout.Version)}}}
	for part := 0; part < parts; part++ {
		end := min(len(body), (part+1)*chunkSize)
		text := lead + fmt.Sprintf("Правила (версия %s, часть %d/%d):\n", oneLine(rules.VersionLabel), part+1, parts) + string(body[part*chunkSize:end])
		if part == parts-1 {
			return p.sendView(ctx, maxID, text, rows)
		}
		if err := p.sendView(ctx, maxID, text, nil); err != nil {
			return err
		}
	}
	return errors.New("rules text is empty")
}

func (p Bootstrap) cancelCheckout(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, checkoutID string, version int64, confirm bool) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка отмены повреждена. Откройте /menu.", nil)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID {
		return p.sendView(ctx, maxID, "Активное оформление не найдено. Откройте /menu.", nil)
	}
	if checkout.Version != version || checkout.Status != "holding" {
		return p.sendView(ctx, maxID, "Оформление изменилось. Обновите /menu.", nil)
	}
	if !confirm {
		rows := [][]maxsdk.Button{{{Text: "Да, отменить", Payload: fmt.Sprintf("cancel:%s:%d", checkout.ID, checkout.Version)}}, {{Text: "Нет, оставить", Payload: "menu"}}}
		return p.sendView(ctx, maxID, "Отменить оформление и освободить машину? Поездка не начата.", rows)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("checkout cancellation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.cancel")
	if err != nil {
		return err
	}
	result, err := p.Commands.CheckoutCancel(ctx, actor, checkout.ID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 && (apiErr.Code == "HOLD_EXPIRED" || apiErr.Code == "STALE_VERSION" || apiErr.Code == "INVALID_STATE") {
			return p.sendView(ctx, maxID, "Оформление уже изменилось или истекло. Обновите /menu.", nil)
		}
		return err
	}
	cancelled, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || cancelled.ID != checkout.ID || cancelled.Status != "cancelled" {
		return errors.New("checkout cancellation returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Оформление отменено. Машина освобождена. Откройте /cars.", [][]maxsdk.Button{{{Text: "Доступные автомобили", Payload: "cars:1"}}})
}

func parkingMapURL(parking dataapi.ParkingLocation) string {
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", parking.Latitude, parking.Longitude, parking.Latitude, parking.Longitude)
}

func vehicleActionTarget(event dataapi.NormalizedEvent, prefix string) (string, int64, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return "", 0, false
	}
	payload := *event.Payload.CallbackData
	if !strings.HasPrefix(payload, prefix) {
		return "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(payload, prefix), ":")
	if len(parts) != 2 {
		return "", 0, true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, true
	}
	return parts[0], version, true
}

func (p Bootstrap) checkoutIntent(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64, confirm bool) error {
	refresh := [][]maxsdk.Button{{{Text: "Обновить список", Payload: "cars:1"}}}
	if !vehicleIDPattern.MatchString(vehicleID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка оформления повреждена. Обновите список.", refresh)
	}
	if state.Checkout != nil {
		if state.Checkout.VehicleID == vehicleID {
			return p.sendView(ctx, maxID, "Оформление уже начато. Hold до "+formatMoment(state.Checkout.ExpiresAt, p.Location)+". Откройте /menu для продолжения.", nil)
		}
		return p.sendView(ctx, maxID, "Сначала завершите текущее оформление. Откройте /menu.", nil)
	}
	if state.Trip != nil || !employee.CanStartTrip {
		return p.sendView(ctx, maxID, "Сейчас нельзя начать оформление автомобиля. Откройте /menu.", nil)
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Автомобиль больше не доступен. Обновите список.", refresh)
		}
		return err
	}
	if vehicle.Version != version || !canOfferCheckout(vehicle, employee, state) {
		return p.sendView(ctx, maxID, "Данные автомобиля изменились или выдача недоступна. Обновите список.", refresh)
	}
	if !confirm {
		text := fmt.Sprintf("Подтвердите оформление %s. После подтверждения машина резервируется на 15 минут; поездка ещё не начнётся.", oneLine(vehicle.Plate))
		rows := [][]maxsdk.Button{{{Text: "Подтвердить", Payload: fmt.Sprintf("take:%s:%d", vehicle.ID, vehicle.Version)}}, {{Text: "Назад к карточке", Payload: fmt.Sprintf("car:%s:%d", vehicle.ID, vehicle.Version)}}}
		return p.sendView(ctx, maxID, text, rows)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("checkout command requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.create")
	if err != nil {
		return err
	}
	result, err := p.Commands.CheckoutCreate(ctx, actor, vehicle.ID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 && (apiErr.Code == "STALE_VERSION" || apiErr.Code == "VEHICLE_UNAVAILABLE" || apiErr.Code == "USER_BUSY") {
			return p.sendView(ctx, maxID, "Машина уже недоступна или оформление изменилось. Обновите /menu и список.", refresh)
		}
		return err
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || checkout.ID == "" || checkout.VehicleID != vehicle.ID || checkout.ExpiresAt.IsZero() {
		return errors.New("checkout command returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Машина зарезервирована до "+formatMoment(checkout.ExpiresAt, p.Location)+". Поездка ещё не началась. Откройте /menu для продолжения оформления.", nil)
}

func canOfferCheckout(vehicle dataapi.Vehicle, employee dataapi.Employee, state dataapi.CurrentState) bool {
	return employee.CanStartTrip && state.Trip == nil && state.Checkout == nil && vehicle.Status == "available" && !vehicle.ManualBlocked && !vehicle.NeedsReview && vehicle.CurrentParking != nil && !vehicle.CurrentParking.ConfirmedAt.IsZero() && strings.TrimSpace(vehicle.KeyInstructions) != ""
}

func checkoutAvailabilityText(vehicle dataapi.Vehicle, employee dataapi.Employee, state dataapi.CurrentState) string {
	if !employee.CanStartTrip || state.Trip != nil || state.Checkout != nil {
		return "Выдача: недоступна для текущего пользователя или пока не завершён текущий сценарий."
	}
	if vehicle.Status != "available" || vehicle.ManualBlocked || vehicle.NeedsReview {
		return "Выдача: автомобиль сейчас недоступен. Обновите список."
	}
	if !canOfferCheckout(vehicle, employee, state) {
		return "Выдача: требуется подтверждённая парковка и инструкция по ключам."
	}
	return "Выдача: доступно подтверждение оформления; поездка начнётся только после приёмки."
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
		if err != nil || page < 1 || page > 20 {
			return 0, true
		}
		return page, true
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
	if err != nil || page < 1 || page > 20 {
		return 0, true
	}
	return page, true
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
	if state.Checkout != nil && state.Checkout.Status == "holding" {
		rows := [][]maxsdk.Button{}
		if state.Checkout.Step == "math" {
			rows = append(rows, []maxsdk.Button{{Text: "Продолжить оформление", Payload: fmt.Sprintf("math:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
		}
		if state.Checkout.Step == "rules" {
			rows = append(rows, []maxsdk.Button{{Text: "Прочитать правила", Payload: fmt.Sprintf("rules:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
		}
		if state.Checkout.Step == "inspection" {
			rows = append(rows, []maxsdk.Button{{Text: "Продолжить фото", Payload: fmt.Sprintf("photos:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
		}
		return append(rows, []maxsdk.Button{{Text: "Отменить оформление", Payload: fmt.Sprintf("cancel-intent:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
	}
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
