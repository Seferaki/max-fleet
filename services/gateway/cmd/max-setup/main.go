package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

var webhookSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,256}$`)

var requiredUpdateTypes = []string{"message_created", "message_callback", "bot_started"}

type subscriptionManager interface {
	GetSubscriptions(context.Context) (model.GetSubscriptionsResult, error)
	Subscribe(context.Context, string, string, []string, string) (model.SimpleQueryResult, error)
}

func main() {
	if os.Getenv("MAX_FLEET_CONFIGURE_MAX_WEBHOOK") != "1" {
		log.Fatal("set MAX_FLEET_CONFIGURE_MAX_WEBHOOK=1 to enable the guarded MAX account change")
	}
	if err := requireWebhookMode(os.Getenv("MAX_UPDATE_MODE")); err != nil {
		log.Fatal(err)
	}
	token, err := readSecretFile("MAX_BOT_TOKEN_FILE")
	if err != nil {
		log.Fatal("MAX bot token file is unavailable")
	}
	secret, err := readSecretFile("MAX_WEBHOOK_SECRET_FILE")
	if err != nil {
		log.Fatal("MAX webhook secret file is unavailable")
	}
	api, err := maxsdk.New(token)
	if err != nil {
		log.Fatal("MAX API client is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := ensureWebhook(ctx, api.Subscriptions, os.Getenv("PUBLIC_BASE_URL"), secret, probePublicWeb); err != nil {
		log.Fatal(err)
	}
	fmt.Println("MAX webhook subscription configured and verified")
}

func requireWebhookMode(mode string) error {
	if strings.TrimSpace(mode) != "webhook" {
		return errors.New("MAX_UPDATE_MODE must be webhook before configuring the subscription")
	}
	return nil
}

func ensureWebhook(ctx context.Context, api subscriptionManager, publicBaseURL, secret string, probe func(string) error) error {
	healthURL, webhookURL, err := webhookURLs(publicBaseURL, secret)
	if err != nil {
		return err
	}
	if probe == nil {
		return errors.New("public HTTPS readiness probe is required")
	}
	if err := probe(healthURL); err != nil {
		return err
	}
	current, err := api.GetSubscriptions(ctx)
	if err != nil {
		return errors.New("could not safely read current MAX subscriptions")
	}
	if len(current.Subscriptions) != 0 {
		return errors.New("MAX already has webhook subscriptions; no account change was made")
	}
	result, err := api.Subscribe(ctx, webhookURL, strings.TrimSpace(secret), requiredUpdateTypes, "")
	if err != nil || !result.Success {
		return errors.New("MAX did not confirm webhook subscription")
	}
	verified, err := api.GetSubscriptions(ctx)
	if err != nil || len(verified.Subscriptions) != 1 {
		return errors.New("webhook registration was sent but could not be verified; inspect MAX subscriptions")
	}
	got := verified.Subscriptions[0]
	if got.URL != webhookURL || !sameStrings(got.UpdateTypes, requiredUpdateTypes) {
		return errors.New("MAX returned a different webhook configuration; no further changes were made")
	}
	return nil
}

func webhookURLs(publicBaseURL, secret string) (healthURL, webhookURL string, err error) {
	parsed, parseErr := url.Parse(strings.TrimSpace(publicBaseURL))
	if parseErr != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Port() != "" && parsed.Port() != "443") || (parsed.Path != "" && parsed.Path != "/") {
		return "", "", errors.New("PUBLIC_BASE_URL must be an HTTPS origin on port 443")
	}
	if !webhookSecretPattern.MatchString(strings.TrimSpace(secret)) {
		return "", "", errors.New("MAX webhook secret has an invalid format")
	}
	base := strings.TrimRight(parsed.String(), "/")
	return base + "/health/ready", base + "/max/webhook", nil
}

func probePublicWeb(healthURL string) error {
	parsed, err := url.Parse(healthURL)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("public readiness probe requires HTTPS")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Get(healthURL)
	if err != nil {
		return errors.New("public HTTPS readiness probe failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("public HTTPS readiness returned HTTP %d", response.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4097)).Decode(&body); err != nil || body.Status != "static_ready" {
		return errors.New("public HTTPS endpoint did not return the expected web readiness response")
	}
	return nil
}

func readSecretFile(envName string) (string, error) {
	path := strings.TrimSpace(os.Getenv(envName))
	if path == "" {
		return "", errors.New("secret file path is missing")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(contents))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("secret file is empty or malformed")
	}
	return value, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	wanted := make(map[string]int, len(right))
	for _, value := range right {
		wanted[value]++
	}
	for _, value := range left {
		if wanted[value] == 0 {
			return false
		}
		wanted[value]--
	}
	return true
}
