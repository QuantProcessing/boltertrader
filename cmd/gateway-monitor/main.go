package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/clients/gateway"
	"github.com/QuantProcessing/boltertrader/services/monitor"
	"github.com/shopspring/decimal"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv("GATEWAY_TOKEN"), os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, token string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("gateway-monitor", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	address := flags.String("gateway", "http://127.0.0.1:18080", "Gateway URL; GATEWAY_TOKEN supplies its service token")
	statePath := flags.String("state", "", "required local checkpoint path; parent directory must exist")
	window := flags.Duration("window", time.Minute, "maximum price lookback (2s..1h, whole seconds)")
	threshold := flags.String("change-percent", "5", "absolute midpoint change threshold in percent (5 means 5%)")
	samples := flags.Int("min-samples", 5, "minimum distinct seconds of valid quotes before price evaluation")
	gap := flags.Duration("max-gap", 10*time.Second, "maximum source quote age/interval before interruption")
	cooldown := flags.Duration("cooldown", 5*time.Minute, "minimum interval between price alerts per instrument")
	duration := flags.Duration("duration", 0, "optional bounded run; 0 runs until interrupted")
	var instruments []string
	flags.Func("instrument", "exact Gateway instrument ID; repeat for multiple instruments", func(value string) error {
		instruments = append(instruments, value)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *duration < 0 || *statePath == "" || len(*threshold) > 64 {
		return errors.New("invalid monitor arguments; -state and at least one -instrument are required")
	}
	change, err := decimal.NewFromString(*threshold)
	if err != nil {
		return errors.New("change-percent must be a decimal")
	}
	config := monitor.Config{Source: strings.TrimRight(*address, "/"), Instruments: instruments, Window: *window, ChangePercent: change, MinSamples: *samples, MaxGap: *gap, Cooldown: *cooldown}
	if err := config.Validate(); err != nil {
		return err
	}
	client, err := gateway.New(gateway.Config{URL: *address, Token: token})
	if err != nil {
		return err
	}
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	return monitor.Run(ctx, client, config, *statePath, output)
}
