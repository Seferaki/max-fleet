package dialog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/mapapi"
)

func TestManualMapHTTPWithDataMockAndFreshReturn(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClientsClock(t, func() time.Time { return now })
	defer closeServer()
	const driver, botToken = "8000000000000000001", "synthetic-map-bot-token"
	draft := readyChecklistDraft(t, actor, driver)
	verifier, err := mapapi.NewVerifier(botToken, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	handler, err := mapapi.NewHandler(verifier, actor, mapapi.Point{Latitude: 55.75, Longitude: 37.62})
	if err != nil {
		t.Fatal(err)
	}
	signed := func(actorID string) string {
		user := fmt.Sprintf(`{"id":%s}`, actorID)
		check := fmt.Sprintf("auth_date=%d\nuser=%s", now.Unix(), user)
		secret := hmac.New(sha256.New, []byte("WebAppData"))
		_, _ = secret.Write([]byte(botToken))
		signature := hmac.New(sha256.New, secret.Sum(nil))
		_, _ = signature.Write([]byte(check))
		return url.Values{"auth_date": {fmt.Sprint(now.Unix())}, "user": {user}, "hash": {hex.EncodeToString(signature.Sum(nil))}}.Encode()
	}
	raw := signed(driver)
	get := func(auth string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/returns/"+draft.ID+"/context", nil)
		r.SetPathValue("id", draft.ID)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		handler.Context(w, r)
		return w
	}
	if got := get("MaxInitData " + strings.Replace(raw, driver, "8000000000000000002", 1)); got.Code != http.StatusUnauthorized {
		t.Fatalf("forged signed actor = %d", got.Code)
	}
	if got := get("MaxInitData " + signed("8000000000000000002")); got.Code != http.StatusNotFound {
		t.Fatalf("signed foreign actor = %d", got.Code)
	}
	before := get("MaxInitData " + raw)
	var initial struct {
		Data mapapi.Context `json:"data"`
	}
	if before.Code != http.StatusOK || json.Unmarshal(before.Body.Bytes(), &initial) != nil || initial.Data.Selected || initial.Data.SelectedPoint != nil {
		t.Fatalf("initial map selected without action: %d %s", before.Code, before.Body.String())
	}
	save := func(version int64, key string) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"expected_version":%d,"latitude":55.8,"longitude":37.8,"confirmed":true}`, version)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/returns/"+draft.ID+"/location", strings.NewReader(body))
		r.SetPathValue("id", draft.ID)
		r.Header.Set("Authorization", "MaxInitData "+raw)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.Location(w, r)
		return w
	}
	if got := save(draft.Version-1, "stale-map-key-1"); got.Code != http.StatusConflict {
		t.Fatalf("stale map version = %d", got.Code)
	}
	first := save(draft.Version, "stable-map-key-1")
	if first.Code != http.StatusOK {
		t.Fatalf("manual location save = %d %s", first.Code, first.Body.String())
	}
	if again := save(draft.Version, "stable-map-key-1"); again.Code != http.StatusOK {
		t.Fatalf("lost response retry = %d %s", again.Code, again.Body.String())
	}
	state, err := actor.State(context.Background(), driver)
	if err != nil || state.Return == nil || state.Return.Version != draft.Version+1 || state.Return.ParkingLocation == nil || state.Return.ParkingLocation.Source != "manual_map" {
		t.Fatalf("map command repeated or missing: %+v %v", state.Return, err)
	}
	reopened := get("MaxInitData " + raw)
	var restored struct {
		Data mapapi.Context `json:"data"`
	}
	if reopened.Code != http.StatusOK || json.Unmarshal(reopened.Body.Bytes(), &restored) != nil || !restored.Data.Selected || restored.Data.SelectedPoint == nil || restored.Data.SelectedPoint.Latitude != 55.8 {
		t.Fatalf("reopen did not restore marker: %d %s", reopened.Code, reopened.Body.String())
	}
	if _, err := actor.ReturnCancel(context.Background(), driver, draft.ID, state.Return.Version, "cancel-after-map-1", nil); err != nil {
		t.Fatal(err)
	}
	if old := get("MaxInitData " + raw); old.Code != http.StatusNotFound {
		t.Fatalf("cancelled return remained open: %d", old.Code)
	}
}
