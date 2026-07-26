package main

import (
	"context"
	"os"

	"github.com/QuantProcessing/boltertrader/gateway/observability"
)

func main() {
	logger := observability.NewSafeLogger(os.Stderr)
	defer func() { _ = logger.Sync() }()
	if err := run(context.Background(), os.Args[1:], runtimeDeps{}); err != nil {
		logger.Error("gateway_exit", "startup_or_runtime_failed", "")
		os.Exit(1)
	}
	logger.Info("gateway_exit", "clean", "")
}
