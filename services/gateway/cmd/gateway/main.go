package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxpoll"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxwebhook"
	"github.com/Seferaki/max-fleet/services/gateway/internal/skeleton"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := os.Getenv("GATEWAY_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	mode := os.Getenv("MAX_UPDATE_MODE")
	if mode == "" || mode == "disabled" {
		if err := skeleton.Run(ctx, addr, "gateway"); err != nil {
			log.Fatal(err)
		}
		return
	}
	var handler http.Handler
	switch mode {
	case "webhook":
		var err error
		handler, err = webhookHandler(ctx)
		if err != nil {
			log.Fatal(err)
		}
	case "polling":
		var err error
		var runner maxpoll.Runner
		handler, runner, err = pollingSetup()
		if err != nil {
			log.Fatal(err)
		}
		go pollingLoop(ctx, runner)
	default:
		log.Fatal("gateway: MAX_UPDATE_MODE must be disabled, webhook or polling")
	}
	if err := skeleton.Serve(ctx, addr, "gateway", handler); err != nil {
		log.Fatal(err)
	}
}

func webhookHandler(ctx context.Context) (http.Handler, error) {
	integrationKey, err := integrationKey()
	if err != nil {
		return nil, err
	}
	secret, err := readSecretFile("MAX_WEBHOOK_SECRET_FILE")
	if err != nil {
		return nil, err
	}
	worker, err := workerClient()
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
		actorToken, err := readSecretFile("DATA_API_TOKEN_FILE")
		if err != nil {
			return nil, err
		}
		client, err := dataapi.New(dataapi.Config{BaseURL: os.Getenv("DATA_API_BASE_URL"), Token: actorToken})
		if err != nil {
			return nil, errors.New("gateway: invalid DataAPI configuration")
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		meta, err := client.Meta(checkCtx)
		if err != nil || meta.Mode != "real" {
			return nil, errors.New("gateway: production requires a real DataAPI")
		}
	}
	webhook, err := maxwebhook.New(secret, integrationKey, worker)
	if err != nil {
		return nil, err
	}
	mux := diagnosticsHandler()
	mux.Handle("POST /max/webhook", webhook)
	mux.HandleFunc("GET /health/max-events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(webhook.Stats())
	})
	return mux, nil
}

func pollingSetup() (http.Handler, maxpoll.Runner, error) {
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
		return nil, maxpoll.Runner{}, errors.New("gateway: polling is development-only")
	}
	key, err := integrationKey()
	if err != nil {
		return nil, maxpoll.Runner{}, err
	}
	worker, err := workerClient()
	if err != nil {
		return nil, maxpoll.Runner{}, err
	}
	token, err := readSecretFile("MAX_BOT_TOKEN_FILE")
	if err != nil {
		return nil, maxpoll.Runner{}, err
	}
	api, err := maxsdk.New(token)
	if err != nil {
		return nil, maxpoll.Runner{}, errors.New("gateway: invalid MAX SDK configuration")
	}
	source, err := maxsdk.NewUpdateSource(api)
	if err != nil {
		return nil, maxpoll.Runner{}, err
	}
	return diagnosticsHandler(), maxpoll.Runner{IntegrationKey: key, WorkerID: "gateway-dev-poller", Source: source, Store: worker}, nil
}

func pollingLoop(ctx context.Context, runner maxpoll.Runner) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		result, err := runner.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Print("gateway: polling cycle failed; retrying")
		} else if err == nil && result.Ignored > 0 {
			log.Printf("gateway: polling ignored %d unsupported events", result.Ignored)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func integrationKey() (string, error) {
	key := os.Getenv("MAX_INTEGRATION_KEY")
	if key == "" {
		if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
			return "", errors.New("gateway: MAX_INTEGRATION_KEY required in production")
		}
		return "demo-bot", nil
	}
	return key, nil
}

func workerClient() (*dataapi.WorkerClient, error) {
	token, err := readSecretFile("WORKER_API_TOKEN_FILE")
	if err != nil {
		return nil, err
	}
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: os.Getenv("DATA_API_BASE_URL"), Token: token})
	if err != nil {
		return nil, errors.New("gateway: invalid worker DataAPI configuration")
	}
	return worker, nil
}

func diagnosticsHandler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"partial","reason":"inbox worker not started"}`))
	})
	return mux
}

func readSecretFile(name string) (string, error) {
	path := os.Getenv(name)
	if path == "" {
		return "", errors.New("gateway: required secret file is missing")
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("gateway: cannot read required secret file")
	}
	value := strings.TrimSpace(string(bytes))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("gateway: required secret file is invalid")
	}
	return value, nil
}
