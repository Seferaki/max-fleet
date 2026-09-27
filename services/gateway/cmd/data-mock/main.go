package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/Seferaki/max-fleet/services/gateway/internal/skeleton"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := os.Getenv("DATA_MOCK_LISTEN_ADDR")
	if addr == "" {
		addr = ":8000"
	}
	if err := skeleton.Run(ctx, addr, "data-mock"); err != nil {
		log.Fatal(err)
	}
}
