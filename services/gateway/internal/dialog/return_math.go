package dialog

import (
	"context"
	"errors"
	"fmt"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
)

func (p Bootstrap) createReturnMath(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, returnID string, version int64) error {
	if !vehicleIDPattern.MatchString(returnID) || version < 1 || !returnDraftMatches(state, employee, stateTripID(state)) || state.Return.ID != returnID || state.Return.Version != version || state.Return.Step != "math" || state.Trip.Version < 2 {
		return p.sendView(ctx, maxID, "Шаг возврата изменился. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return challenge requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "challenge.create")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeCreateReturn(ctx, actor, returnID, version, state.Trip.ID, state.Trip.Version-1, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Вопрос возврата устарел. Обновите /menu.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || !validReturnChallenge(challenge) {
		return errors.New("return challenge returned invalid aggregate")
	}
	return p.sendChallenge(ctx, maxID, challenge, "Подтвердите возврат: выберите ответ. Попыток: 3.")
}

func stateTripID(state dataapi.CurrentState) string {
	if state.Trip != nil {
		return state.Trip.ID
	}
	return ""
}

func validReturnChallenge(challenge dataapi.Challenge) bool {
	return vehicleIDPattern.MatchString(challenge.ID) && challenge.Purpose == "return" && challenge.Question != "" && len(challenge.Options) == 4 && challenge.Version > 0 && challenge.AttemptsRemaining >= 0 && challenge.AttemptsRemaining <= 3 && !challenge.ExpiresAt.IsZero()
}

func (p Bootstrap) answerReturnMath(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, challengeID string, version int64, option int) error {
	if !vehicleIDPattern.MatchString(challengeID) || version < 1 || !returnDraftMatches(state, employee, stateTripID(state)) {
		return p.sendView(ctx, maxID, "Вопрос возврата недоступен. Обновите /menu.", nil)
	}
	if state.Return.Step == "checklist" {
		return p.sendView(ctx, maxID, "Проверка возврата уже пройдена. Продолжите через /menu.", nil)
	}
	if state.Return.Step != "math" {
		return p.sendView(ctx, maxID, "Шаг возврата изменился. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("return challenge answer requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "challenge.answer")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeAnswer(ctx, actor, challengeID, version, option, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Ответ устарел или вопрос недоступен. Откройте /menu.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || !validReturnChallenge(challenge) || challenge.ID != challengeID || result.Correct == nil || result.AttemptsRemaining == nil || *result.AttemptsRemaining != challenge.AttemptsRemaining {
		return errors.New("return challenge answer returned invalid aggregate")
	}
	if *result.Correct {
		current, err := p.Data.State(ctx, actor)
		if err != nil {
			return err
		}
		if !returnDraftMatches(current, employee, state.Trip.ID) || current.Return.Step != "checklist" || current.Return.IntentConfirmedAt == nil {
			return errors.New("return challenge success did not advance draft")
		}
		return p.sendView(ctx, maxID, "Проверка возврата пройдена. Продолжите осмотр через /menu.", nil)
	}
	if *result.AttemptsRemaining == 0 {
		return p.sendView(ctx, maxID, "Три неверных ответа. Получите новый вопрос через /menu.", nil)
	}
	return p.sendChallenge(ctx, maxID, challenge, fmt.Sprintf("Ответ неверный. Осталось попыток: %d.", *result.AttemptsRemaining))
}
