package maxsdk

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
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
	t.Logf("MAX /me PASS: bot=%q username=%q", bot.FirstName, bot.Username)
}
