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

	addr := os.Getenv("GATEWAY_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if err := skeleton.Run(ctx, addr, "gateway"); err != nil {
		log.Fatal(err)
	}
}
