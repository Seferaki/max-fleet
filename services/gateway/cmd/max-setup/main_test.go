package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

type fakeSubscriptions struct {
	current        []model.Subscription
	gets           int
	subscribeCalls int
	url            string
	secret         string
	updateTypes    []string
	result         model.SimpleQueryResult
	err            error
}

func (f *fakeSubscriptions) GetSubscriptions(context.Context) (model.GetSubscriptionsResult, error) {
	f.gets++
	return model.GetSubscriptionsResult{Subscriptions: append([]model.Subscription(nil), f.current...)}, f.err
}

func (f *fakeSubscriptions) Subscribe(_ context.Context, endpoint, secret string, updateTypes []string, _ string) (model.SimpleQueryResult, error) {
	f.subscribeCalls++
	f.url = endpoint
	f.secret = secret
	f.updateTypes = append([]string(nil), updateTypes...)
	if f.err == nil && f.result.Success {
		f.current = []model.Subscription{{URL: endpoint, UpdateTypes: append([]string(nil), updateTypes...)}}
	}
	return f.result, f.err
}

func TestEnsureWebhookCreatesAndVerifiesSubscription(t *testing.T) {
	api := &fakeSubscriptions{result: model.SimpleQueryResult{Success: true}}
	var probed string
	err := ensureWebhook(context.Background(), api, "https://fleet.example", "secret_value-123", func(url string) error {
		probed = url
		return nil
	})
	if err != nil {
		t.Fatalf("ensureWebhook() error = %v", err)
	}
	if probed != "https://fleet.example/health/ready" || api.url != "https://fleet.example/max/webhook" {
		t.Fatalf("preflight or registered endpoint is wrong")
	}
	if api.gets != 2 || api.subscribeCalls != 1 || api.secret != "secret_value-123" || !reflect.DeepEqual(api.updateTypes, requiredUpdateTypes) {
		t.Fatalf("subscription calls did not match expected safe flow")
	}
}

func TestEnsureWebhookRefusesExistingSubscription(t *testing.T) {
	api := &fakeSubscriptions{current: []model.Subscription{{URL: "https://other.example/max/webhook"}}}
	err := ensureWebhook(context.Background(), api, "https://fleet.example", "secret_value-123", func(string) error { return nil })
	if err == nil || api.subscribeCalls != 0 {
		t.Fatal("existing subscription was overwritten")
	}
}

func TestEnsureWebhookRequiresHealthyPublicHTTPSBeforeMutation(t *testing.T) {
	api := &fakeSubscriptions{result: model.SimpleQueryResult{Success: true}}
	err := ensureWebhook(context.Background(), api, "https://fleet.example", "secret_value-123", func(string) error {
		return errors.New("synthetic probe failure")
	})
	if err == nil || api.gets != 0 || api.subscribeCalls != 0 {
		t.Fatal("MAX was contacted before public HTTPS was confirmed")
	}
}

func TestWebhookURLsRejectUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct {
		baseURL string
		secret  string
	}{
		{baseURL: "http://fleet.example", secret: "secret_value-123"},
		{baseURL: "https://fleet.example:8443", secret: "secret_value-123"},
		{baseURL: "https://user@fleet.example", secret: "secret_value-123"},
		{baseURL: "https://fleet.example/path", secret: "secret_value-123"},
		{baseURL: "https://fleet.example", secret: "tiny"},
		{baseURL: "https://fleet.example", secret: "secret/value"},
	} {
		if _, _, err := webhookURLs(test.baseURL, test.secret); err == nil {
			t.Fatalf("webhookURLs(%q) accepted invalid configuration", test.baseURL)
		}
	}
}

func TestEnsureWebhookRequiresExplicitProbe(t *testing.T) {
	api := &fakeSubscriptions{result: model.SimpleQueryResult{Success: true}}
	if err := ensureWebhook(context.Background(), api, "https://fleet.example", "secret_value-123", nil); err == nil {
		t.Fatal("nil public readiness probe was accepted")
	}
}

func TestRequireWebhookMode(t *testing.T) {
	if err := requireWebhookMode("webhook"); err != nil {
		t.Fatalf("configured webhook mode rejected: %v", err)
	}
	for _, mode := range []string{"", "disabled", "polling"} {
		if err := requireWebhookMode(mode); err == nil {
			t.Fatalf("mode %q could configure a webhook while not in webhook mode", mode)
		}
	}
}

func TestProbePublicWebRejectsNonHTTPS(t *testing.T) {
	if err := probePublicWeb("http://fleet.example/health/ready"); err == nil {
		t.Fatal("non-HTTPS health endpoint was accepted")
	}
}
