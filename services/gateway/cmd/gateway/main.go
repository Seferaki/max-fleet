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
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/dialog"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxpoll"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxwebhook"
	"github.com/Seferaki/max-fleet/services/gateway/internal/skeleton"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	inbox, enabled, err := inboxWorkerSetup()
	if err != nil {
		log.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	var workerDone chan struct{}
	if enabled {
		workerDone = make(chan struct{})
		go func() {
			defer close(workerDone)
			if err := inbox.Run(workerCtx, 5*time.Second, 10, observeInbox); err != nil {
				log.Print("gateway: inbox worker stopped with configuration error")
			}
		}()
	}
	serveErr := skeleton.Serve(ctx, addr, "gateway", handler)
	stopWorker()
	if workerDone != nil {
		<-workerDone
	}
	if serveErr != nil {
		log.Fatal(serveErr)
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
	transport, err := maxsdk.NewTransport(api)
	if err != nil {
		return nil, maxpoll.Runner{}, err
	}
	return diagnosticsHandler(), maxpoll.Runner{IntegrationKey: key, WorkerID: "gateway-dev-poller", Source: source, Store: worker, Reject: transport}, nil
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
		if err == nil && result.Rejected > 0 {
			log.Printf("gateway: polling rejected %d multi-attachment events", result.Rejected)
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

func inboxWorkerSetup() (inboxworker.Worker, bool, error) {
	if os.Getenv("MAX_BOT_TOKEN_FILE") == "" {
		if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
			return inboxworker.Worker{}, false, errors.New("gateway: MAX_BOT_TOKEN_FILE required in production")
		}
		return inboxworker.Worker{}, false, nil
	}
	token, err := readSecretFile("MAX_BOT_TOKEN_FILE")
	if err != nil {
		return inboxworker.Worker{}, false, err
	}
	actorToken, err := readSecretFile("DATA_API_TOKEN_FILE")
	if err != nil {
		return inboxworker.Worker{}, false, err
	}
	actor, err := dataapi.New(dataapi.Config{BaseURL: os.Getenv("DATA_API_BASE_URL"), Token: actorToken})
	if err != nil {
		return inboxworker.Worker{}, false, errors.New("gateway: invalid actor DataAPI configuration")
	}
	store, err := workerClient()
	if err != nil {
		return inboxworker.Worker{}, false, err
	}
	api, err := maxsdk.New(token)
	if err != nil {
		return inboxworker.Worker{}, false, errors.New("gateway: invalid MAX SDK configuration")
	}
	sender, err := maxsdk.NewTransport(api)
	if err != nil {
		return inboxworker.Worker{}, false, err
	}
	zone := os.Getenv("COMPANY_TIMEZONE")
	if zone == "" {
		if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
			return inboxworker.Worker{}, false, errors.New("gateway: COMPANY_TIMEZONE required in production")
		}
		zone = "Europe/Moscow"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return inboxworker.Worker{}, false, errors.New("gateway: COMPANY_TIMEZONE is invalid")
	}
	var photoDownloader dialog.PhotoFetcher
	if configured := os.Getenv("MAX_PHOTO_HOSTS"); configured != "" {
		hosts := strings.Split(configured, ",")
		for i := range hosts {
			hosts[i] = strings.TrimSpace(hosts[i])
		}
		photoDownloader, err = maxsdk.NewPhotoDownloader(hosts)
		if err != nil {
			return inboxworker.Worker{}, false, errors.New("gateway: MAX_PHOTO_HOSTS is invalid")
		}
	}
	return inboxworker.Worker{ID: "gateway-inbox-worker", Store: store, Processor: dialog.Bootstrap{Data: actor, Commands: actor, MAX: sender, Photos: photoDownloader, PhotoStore: actor, Location: location}, Now: time.Now}, true, nil
}

func observeInbox(result inboxworker.Result, err error) {
	if err != nil {
		log.Print("gateway: inbox worker cycle failed; retrying")
		return
	}
	if result.Claimed > 0 {
		log.Printf("gateway: inbox claimed=%d acked=%d deferred=%d retried=%d dead=%d", result.Claimed, result.Acked, result.Deferred, result.Retried, result.Dead)
	}
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
		_, _ = w.Write([]byte(`{"status":"partial","reason":"dialog flows incomplete"}`))
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
