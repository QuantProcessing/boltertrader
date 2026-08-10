package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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
	if err := config.ValidateStartupProfile(); err != nil {
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
		ValidationReady: func() error { return writeValidationReadiness(config) },
	})
	if err != nil {
		return err
	}
	log.Printf("funding worker starting worker=%s instance=%s", config.WorkerID, config.InstanceID)
	if config.StartupProfile == "migration-validation" {
		return runtime.ValidateAndWait(ctx)
	}
	return runtime.Run(ctx)
}

func writeValidationReadiness(config funding.Config) error {
	path := config.StateFile + ".migration-validation.ready"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create readiness directory: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"workerId": config.WorkerID, "instanceId": config.InstanceID,
		"profile": "migration-validation", "observedAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("write readiness sentinel: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish readiness sentinel: %w", err)
	}
	return nil
}
