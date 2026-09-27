package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"

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
	server, err := datamock.New(token)
	if err != nil {
		log.Fatal(err)
	}
	if err := skeleton.Serve(ctx, addr, "data-mock", server.Handler()); err != nil {
		log.Fatal(err)
	}
}

func serviceToken() (string, error) {
	value, path := os.Getenv("DATA_API_TOKEN"), os.Getenv("DATA_API_TOKEN_FILE")
	if (value == "") == (path == "") {
		return "", errors.New("data-mock: set exactly one of DATA_API_TOKEN or DATA_API_TOKEN_FILE")
	}
	if path == "" {
		return value, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("data-mock: cannot read DATA_API_TOKEN_FILE")
	}
	return strings.TrimSpace(string(data)), nil
}
