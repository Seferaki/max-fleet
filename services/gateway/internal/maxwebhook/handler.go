package maxwebhook

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync/atomic"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
)

const maxBodyBytes = 256 << 10

type InboxStore interface {
	StoreInbox(context.Context, dataapi.NormalizedEvent, string) (dataapi.InboxStored, error)
}

type Handler struct {
	secret         [32]byte
	integrationKey string
	store          InboxStore
	accepted       atomic.Uint64
	ignored        atomic.Uint64
	unavailable    atomic.Uint64
}

type Stats struct {
	Accepted    uint64 `json:"accepted"`
	Ignored     uint64 `json:"ignored"`
	Unavailable uint64 `json:"unavailable"`
}

func (h *Handler) Stats() Stats {
	return Stats{Accepted: h.accepted.Load(), Ignored: h.ignored.Load(), Unavailable: h.unavailable.Load()}
}

func New(secret, integrationKey string, store InboxStore) (*Handler, error) {
	if len(secret) < 16 || len(secret) > 1024 || integrationKey == "" || len(integrationKey) > 100 || store == nil {
		return nil, errors.New("MAX webhook configuration is invalid")
	}
	return &Handler{secret: sha256.Sum256([]byte(secret)), integrationKey: integrationKey, store: store}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		respond(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	secrets := r.Header.Values(maxbot.SecretHeader)
	if len(secrets) != 1 || subtle.ConstantTimeCompare(h.secret[:], hashSecret(secrets[0])) != 1 {
		respond(w, http.StatusUnauthorized, "INVALID_WEBHOOK_SECRET")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		respond(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		respond(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if len(body) > maxBodyBytes {
		respond(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE")
		return
	}
	update, err := maxsdk.DecodeUpdate(body)
	if err != nil {
		respond(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	event, err := maxsdk.Normalize(h.integrationKey, update)
	if errors.Is(err, maxsdk.ErrUnsupportedUpdate) || errors.Is(err, maxsdk.ErrGroupEvent) {
		h.ignored.Add(1)
		respond(w, http.StatusOK, "IGNORED")
		return
	}
	if err != nil {
		respond(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	key := maxsdk.InboxIdempotencyKey(event)
	if _, err := h.store.StoreInbox(r.Context(), event, key); err != nil {
		h.unavailable.Add(1)
		respond(w, http.StatusServiceUnavailable, "DATA_UNAVAILABLE")
		return
	}
	h.accepted.Add(1)
	respond(w, http.StatusOK, "STORED")
}

func hashSecret(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func respond(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": code})
}
