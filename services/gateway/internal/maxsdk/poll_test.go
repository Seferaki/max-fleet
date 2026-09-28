package maxsdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

func TestSDKUpdateSourceUsesPinnedPollingAPI(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/updates" || r.URL.Query().Get("marker") != "42" || r.Header.Get("Authorization") != "synthetic-test-token" {
			t.Errorf("wrong poll request: method=%s path=%s marker=%s auth=%t", r.Method, r.URL.Path, r.URL.Query().Get("marker"), r.Header.Get("Authorization") == "synthetic-test-token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"updates":[{"update_type":"bot_started","timestamp":1790586000000,"chat_id":123,"user":{"user_id":123}}],"marker":43}`))
	}))
	defer server.Close()
	api, err := maxbot.NewApi("synthetic-test-token", maxbot.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewUpdateSource(api)
	if err != nil {
		t.Fatal(err)
	}
	updates, marker, err := source.GetUpdates(context.Background(), 42)
	if err != nil || calls != 1 || marker != 43 || len(updates) != 1 || updates[0].UpdateType != model.UpdateBotStarted || updates[0].User == nil || updates[0].User.UserID != 123 {
		t.Fatalf("poll result: updates=%+v marker=%d err=%v calls=%d", updates, marker, err, calls)
	}
}

func TestSDKUpdateSourceRejectsInvalidMarker(t *testing.T) {
	if _, err := NewUpdateSource(nil); err == nil {
		t.Fatal("accepted nil SDK")
	}
	source := &SDKUpdateSource{}
	if _, _, err := source.GetUpdates(context.Background(), -1); err == nil {
		t.Fatal("accepted invalid marker")
	}
}
