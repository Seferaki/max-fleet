package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
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
	if mode != "webhook" {
		log.Fatal("gateway: MAX_UPDATE_MODE must be disabled or webhook")
	}
	handler, err := webhookHandler(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := skeleton.Serve(ctx, addr, "gateway", handler); err != nil {
		log.Fatal(err)
	}
}

func webhookHandler(ctx context.Context) (http.Handler, error) {
	integrationKey := os.Getenv("MAX_INTEGRATION_KEY")
	if integrationKey == "" {
		if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
			return nil, errors.New("gateway: MAX_INTEGRATION_KEY required in production")
		}
		integrationKey = "demo-bot"
	}
	secret, err := readSecretFile("MAX_WEBHOOK_SECRET_FILE")
	if err != nil {
		return nil, err
	}
	workerToken, err := readSecretFile("WORKER_API_TOKEN_FILE")
	if err != nil {
		return nil, err
	}
	baseURL := os.Getenv("DATA_API_BASE_URL")
	worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: baseURL, Token: workerToken})
	if err != nil {
		return nil, errors.New("gateway: invalid worker DataAPI configuration")
	}
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
		actorToken, err := readSecretFile("DATA_API_TOKEN_FILE")
		if err != nil {
			return nil, err
		}
		client, err := dataapi.New(dataapi.Config{BaseURL: baseURL, Token: actorToken})
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
	mux := http.NewServeMux()
	mux.Handle("POST /max/webhook", webhook)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"partial","reason":"inbox worker not started"}`))
	})
	return mux, nil
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
