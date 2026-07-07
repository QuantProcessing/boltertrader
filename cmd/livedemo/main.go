// Command livedemo assembles the real Binance USD-M perpetual adapter into a
// TradingNode running an example strategy. It is the end-to-end "it's a real
// system now" wiring for P7.
//
// It is env-gated: without BINANCE_API_KEY / BINANCE_API_SECRET it prints usage
// and exits 0, so it is safe to build and run in CI without network or secrets.
//
//	BINANCE_API_KEY=... BINANCE_API_SECRET=... \
//	BT_SYMBOL=BTC-USDT BT_QTY=0.001 \
//	BT_JOURNAL_PATH=.boltertrader/livedemo.journal go run ./cmd/livedemo
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/binance/perp"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/strategy/strategies"
	"github.com/shopspring/decimal"
)

func main() {
	apiKey := os.Getenv("BINANCE_API_KEY")
	apiSecret := os.Getenv("BINANCE_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		log.Println("livedemo: set BINANCE_API_KEY and BINANCE_API_SECRET to run.")
		log.Println("This is a live demo; without credentials it exits without connecting.")
		return
	}
	accountID := os.Getenv("BT_ACCOUNT_ID")

	symbol := getenv("BT_SYMBOL", "BTC-USDT")
	journalPath := getenv("BT_JOURNAL_PATH", filepath.Join(".boltertrader", "livedemo.journal"))
	qty := decimal.Zero
	if v := os.Getenv("BT_QTY"); v != "" {
		qty = decimal.RequireFromString(v)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	clk := clock.NewRealClock()

	log.Println("building Binance adapter (loading exchangeInfo)...")
	adapter, err := perp.New(ctx, perp.Config{APIKey: apiKey, APISecret: apiSecret, Clock: clk})
	if err != nil {
		log.Fatalf("adapter: %v", err)
	}
	defer adapter.Close()
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o755); err != nil {
		log.Fatalf("create journal dir: %v", err)
	}
	journalStore, err := journal.OpenFile(journalPath, journal.FileOptions{})
	if err != nil {
		log.Fatalf("open journal: %v", err)
	}
	defer journalStore.Close()

	inst := model.InstrumentID{Venue: "BINANCE", Symbol: symbol, Kind: enums.KindPerp}

	strat := &strategies.PrintTrades{
		Instrument: inst,
		BuyOnceQty: qty,
		Logf:       log.Printf,
	}

	nodeOpts := []runtime.Option{
		runtime.WithStrategy(strat),
		runtime.WithBars(inst, time.Minute, "1m"),
		runtime.WithJournal(journalStore),
	}
	if accountID != "" {
		nodeOpts = append(nodeOpts, runtime.WithAccountID(accountID))
	}

	node := runtime.NewNode(
		runtime.Clients{
			Market:    adapter.Market,
			Execution: adapter.Execution,
			Account:   adapter.Account,
		},
		clk, "livedemo",
		nodeOpts...,
	)

	// Pre-trade risk: cap a single order and the resulting position.
	riskEng := risk.New(risk.Limits{
		MaxOrderQty:    decimal.RequireFromString("0.01"),
		MaxPositionQty: decimal.RequireFromString("0.05"),
	}, node.Cache)
	runtime.WithRisk(riskEng, adapter.Market.InstrumentProvider())(node)

	// Reconcile cache from REST before trading.
	log.Println("reconciling account state...")
	if rep, err := node.Resync(ctx); err != nil {
		log.Printf("resync warning: %v", err)
	} else {
		log.Printf("resync: balances=%d positions=%d", rep.BalancesUpdated, rep.PositionsUpdated)
	}

	// Start the private user-data stream and subscribe to public trades.
	if err := adapter.Start(ctx); err != nil {
		log.Fatalf("start user-data stream: %v", err)
	}
	if err := adapter.Market.SubscribeTrades(ctx, inst); err != nil {
		log.Fatalf("subscribe trades: %v", err)
	}

	log.Printf("running. symbol=%s qty=%s. Ctrl-C to stop.", symbol, qty)
	node.Run(ctx)
	log.Println("stopped.")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
