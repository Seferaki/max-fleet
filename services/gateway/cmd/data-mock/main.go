package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/datamock"
	"github.com/Seferaki/max-fleet/services/gateway/internal/skeleton"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := os.Getenv("DATA_MOCK_LISTEN_ADDR")
	if addr == "" {
		addr = ":8000"
	}
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") {
		log.Fatal("data-mock cannot run in production")
	}
	token, err := serviceToken()
	if err != nil {
		log.Fatal(err)
	}
	workerToken, err := readToken("WORKER_API_TOKEN", "WORKER_API_TOKEN_FILE")
	if err != nil {
		log.Fatal(err)
	}
	snapshotPath := os.Getenv("DATA_MOCK_SNAPSHOT_FILE")
	server, err := datamock.NewWithSnapshotAndWorkerToken(token, workerToken, snapshotPath, time.Now)
	if err != nil {
		log.Fatal(err)
	}
	if err := skeleton.Serve(ctx, addr, "data-mock", server.Handler()); err != nil {
		log.Fatal(err)
	}
}

func serviceToken() (string, error) {
	return readToken("DATA_API_TOKEN", "DATA_API_TOKEN_FILE")
}

func readToken(valueName, fileName string) (string, error) {
	value, path := os.Getenv(valueName), os.Getenv(fileName)
	if (value == "") == (path == "") {
		return "", errors.New("data-mock: set exactly one token source")
	}
	if path == "" {
		if strings.ContainsAny(value, "\r\n") {
			return "", errors.New("data-mock: invalid token")
		}
		return value, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("data-mock: cannot read token file")
	}
	value = strings.TrimSpace(string(data))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("data-mock: invalid token file")
	}
	return value, nil
}
