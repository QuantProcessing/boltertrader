// gateway-market is a read-only application example. It only calls Gateway;
// it never loads venue credentials or creates exchange clients.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	gateway "github.com/QuantProcessing/boltertrader/clients/gateway"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv("GATEWAY_TOKEN"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, token string, out io.Writer) error {
	flags := flag.NewFlagSet("gateway-market", flag.ContinueOnError)
	address := flags.String("gateway", "http://127.0.0.1:18080", "Gateway capability listener")
	instrument := flags.String("instrument", "", "opaque instrument_id from the catalog; empty lists catalog")
	stream := flags.Bool("stream", false, "subscribe to BBO updates, including explicit gaps")
	duration := flags.Duration("duration", 30*time.Second, "maximum run duration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *duration <= 0 || flags.NArg() != 0 {
		return fmt.Errorf("duration and arguments are invalid")
	}
	client, err := gateway.New(gateway.Config{URL: *address, Token: token})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	encoder := json.NewEncoder(out)
	if *instrument == "" {
		catalog, err := client.MarketCatalog(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(catalog)
	}
	topic := gateway.MarketTopic{Topic: gateway.BestBidOffer, InstrumentID: *instrument}
	if !*stream {
		snapshot, err := client.MarketSnapshot(ctx, topic)
		if err != nil {
			return err
		}
		return encoder.Encode(snapshot)
	}
	subscription, err := client.SubscribeMarkets(ctx, []gateway.MarketTopic{topic}, gateway.StreamOptions{})
	if err != nil {
		return err
	}
	defer subscription.Close()
	for event := range subscription.Events() {
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	return subscription.Err()
}
