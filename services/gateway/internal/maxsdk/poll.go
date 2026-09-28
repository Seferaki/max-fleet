package maxsdk

import (
	"context"
	"errors"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// UpdateSource is only used by the opt-in development poller.
type UpdateSource interface {
	GetUpdates(context.Context, int64) ([]model.Update, int64, error)
}

type SDKUpdateSource struct {
	subscriptions updateSource
}

type updateSource interface {
	GetUpdates(context.Context, int64) ([]model.Update, int64, error)
}

func NewUpdateSource(api *maxbot.Api) (*SDKUpdateSource, error) {
	if api == nil || api.Subscriptions == nil {
		return nil, errors.New("MAX subscriptions client is missing")
	}
	return &SDKUpdateSource{subscriptions: api.Subscriptions}, nil
}

func (s *SDKUpdateSource) GetUpdates(ctx context.Context, marker int64) ([]model.Update, int64, error) {
	if s == nil || s.subscriptions == nil || marker < 0 {
		return nil, 0, errors.New("invalid MAX poller or marker")
	}
	return s.subscriptions.GetUpdates(ctx, marker)
}
