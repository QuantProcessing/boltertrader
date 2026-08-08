package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	config, err := funding.ConfigFromEnvironment()
	if err != nil {
		return err
	}
	client, err := funding.NewClient(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	identities, err := funding.NewFileIdentityStore(config.StateFile, config.WorkerID, config.InstanceID)
	if err != nil {
		return err
	}
	admission, err := config.Admission()
	if err != nil {
		return err
	}
	processor, err := funding.NewPayloadProcessor(config.Mode, admission)
	if err != nil {
		return err
	}
	runtime, err := funding.NewRuntime(funding.RuntimeOptions{
		Client: client, Processor: processor, Identities: identities, ClaimWaitSeconds: 20,
	})
	if err != nil {
		return err
	}
	log.Printf("funding worker starting worker=%s instance=%s", config.WorkerID, config.InstanceID)
	return runtime.Run(ctx)
}
