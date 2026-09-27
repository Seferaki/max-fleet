package maxsdk

import (
	"errors"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
)

// New pins the official MAX SDK constructor used by the future transport.
// The token is never logged or accepted from request data.
func New(token string) (*maxbot.Api, error) {
	if token == "" {
		return nil, errors.New("MAX token is empty")
	}
	return maxbot.NewApi(token)
}
