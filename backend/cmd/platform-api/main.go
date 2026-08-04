package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/QuantProcessing/boltertrader/backend/internal/app"
	"github.com/QuantProcessing/boltertrader/backend/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
