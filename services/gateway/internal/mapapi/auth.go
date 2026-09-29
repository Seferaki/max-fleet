package mapapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidInitData = errors.New("map-api: invalid MAX initData")

const (
	maxInitDataBytes  = 8192
	initDataMaxAge    = time.Hour
	initDataClockSkew = time.Minute
)

type Verifier struct {
	secret []byte
	now    func() time.Time
}

func NewVerifier(botToken string, now func() time.Time) (*Verifier, error) {
	if botToken == "" || strings.ContainsAny(botToken, "\r\n") {
		return nil, errors.New("map-api: bot token required")
	}
	if now == nil {
		now = time.Now
	}
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = mac.Write([]byte(botToken))
	return &Verifier{secret: mac.Sum(nil), now: now}, nil
}

func (v *Verifier) Actor(raw string) (string, error) {
	if v == nil || len(v.secret) != sha256.Size || raw == "" || len(raw) > maxInitDataBytes || strings.ContainsAny(raw, "\r\n") {
		return "", ErrInvalidInitData
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) == 0 {
		return "", ErrInvalidInitData
	}
	for _, entries := range values {
		if len(entries) != 1 {
			return "", ErrInvalidInitData
		}
	}
	hashValues, exists := values["hash"]
	if !exists || len(hashValues) != 1 {
		return "", ErrInvalidInitData
	}
	wanted, err := hex.DecodeString(hashValues[0])
	if err != nil || len(wanted) != sha256.Size {
		return "", ErrInvalidInitData
	}
	delete(values, "hash")
	keys := make([]string, 0, len(values))
	for key := range values {
		if key == "" || strings.ContainsAny(key, "\r\n=") {
			return "", ErrInvalidInitData
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	mac := hmac.New(sha256.New, v.secret)
	_, _ = mac.Write([]byte(strings.Join(lines, "\n")))
	if subtle.ConstantTimeCompare(mac.Sum(nil), wanted) != 1 {
		return "", ErrInvalidInitData
	}
	dateValues, userValues := values["auth_date"], values["user"]
	if len(dateValues) != 1 || len(userValues) != 1 {
		return "", ErrInvalidInitData
	}
	seconds, err := strconv.ParseInt(dateValues[0], 10, 64)
	if err != nil || seconds <= 0 {
		return "", ErrInvalidInitData
	}
	issued := time.Unix(seconds, 0)
	current := v.now()
	if issued.Before(current.Add(-initDataMaxAge)) || issued.After(current.Add(initDataClockSkew)) {
		return "", ErrInvalidInitData
	}
	var user struct {
		ID json.Number `json:"id"`
	}
	if json.Unmarshal([]byte(userValues[0]), &user) != nil {
		return "", ErrInvalidInitData
	}
	id, err := strconv.ParseInt(user.ID.String(), 10, 64)
	if err != nil || id <= 0 {
		return "", ErrInvalidInitData
	}
	return strconv.FormatInt(id, 10), nil
}
