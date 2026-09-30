package maxsdk

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAPIClientTrustsMAXRootWithoutChangingHostTrust(t *testing.T) {
	block, _ := pem.Decode(russianTrustedRootPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("embedded MAX root certificate is not PEM encoded")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse embedded MAX root certificate: %v", err)
	}
	if cert.Subject.CommonName != "Russian Trusted Root CA" || !bytes.Equal(cert.RawSubject, cert.RawIssuer) {
		t.Fatalf("unexpected embedded MAX root certificate: subject=%q issuer=%q", cert.Subject.CommonName, cert.Issuer.CommonName)
	}

	client, err := newHTTPClient()
	if err != nil {
		t.Fatalf("create MAX API HTTP client: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("MAX API client does not have a scoped certificate pool")
	}
	if client.Timeout <= 0 {
		t.Fatal("MAX API client has no request timeout")
	}

	for _, subject := range transport.TLSClientConfig.RootCAs.Subjects() {
		if bytes.Equal(subject, cert.RawSubject) {
			return
		}
	}
	t.Fatal("MAX API certificate pool does not contain the pinned Russian root")
}

func TestLiveMAXMe(t *testing.T) {
	if os.Getenv("MAX_FLEET_LIVE_MAX") != "1" {
		t.Skip("set MAX_FLEET_LIVE_MAX=1 to call the real MAX API")
	}
	path := os.Getenv("MAX_BOT_TOKEN_FILE")
	if path == "" {
		t.Fatal("MAX_BOT_TOKEN_FILE is required for the live MAX check")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read MAX token file: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		t.Fatal("MAX token file is empty")
	}

	api, err := New(token)
	if err != nil {
		t.Fatalf("create MAX API client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bot, err := api.Bots.GetMyInfo(ctx)
	if err != nil {
		t.Fatalf("GET /me failed: %v", err)
	}
	if !bot.IsBot {
		t.Fatal("GET /me returned an account that is not a bot")
	}
	t.Log("MAX /me PASS: bot account verified")
}

func TestLiveMAXSubscriptions(t *testing.T) {
	if os.Getenv("MAX_FLEET_LIVE_MAX_SUBSCRIPTIONS") != "1" {
		t.Skip("set MAX_FLEET_LIVE_MAX_SUBSCRIPTIONS=1 to read MAX webhook subscriptions")
	}
	path := os.Getenv("MAX_BOT_TOKEN_FILE")
	if path == "" {
		t.Fatal("MAX_BOT_TOKEN_FILE is required for the live MAX check")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read MAX token file: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		t.Fatal("MAX token file is empty")
	}

	client, err := newHTTPClient()
	if err != nil {
		t.Fatalf("create MAX API HTTP client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://platform-api2.max.ru/subscriptions", nil)
	if err != nil {
		t.Fatalf("create MAX subscriptions request: %v", err)
	}
	request.Header.Set("Authorization", token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET /subscriptions failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /subscriptions returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		t.Fatal("GET /subscriptions response could not be read safely")
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal("GET /subscriptions returned invalid JSON")
	}
	count, ok := countSubscriptionItems(payload)
	if !ok {
		t.Fatal("GET /subscriptions returned an unexpected response shape")
	}
	t.Logf("MAX /subscriptions PASS: count=%d", count)
}

func countSubscriptionItems(value any) (int, bool) {
	switch typed := value.(type) {
	case []any:
		return len(typed), true
	case map[string]any:
		for _, key := range []string{"subscriptions", "data", "result"} {
			if nested, exists := typed[key]; exists {
				if count, ok := countSubscriptionItems(nested); ok {
					return count, true
				}
			}
		}
	}
	return 0, false
}

func TestCountSubscriptionItems(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		count int
		ok    bool
	}{
		{name: "empty array", value: []any{}, count: 0, ok: true},
		{name: "direct subscriptions", value: []any{map[string]any{}, map[string]any{}}, count: 2, ok: true},
		{name: "wrapped subscriptions", value: map[string]any{"subscriptions": []any{map[string]any{}}}, count: 1, ok: true},
		{name: "data envelope", value: map[string]any{"data": []any{}}, count: 0, ok: true},
		{name: "unexpected response", value: map[string]any{"message": "invalid"}, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, ok := countSubscriptionItems(test.value)
			if count != test.count || ok != test.ok {
				t.Fatalf("countSubscriptionItems() = (%d, %t), want (%d, %t)", count, ok, test.count, test.ok)
			}
		})
	}
}
