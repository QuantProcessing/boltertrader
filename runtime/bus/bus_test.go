package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
)

// TestFanInAllSources verifies events from all three channels reach their
// handlers, and that Run returns once every channel is closed.
func TestFanInAllSources(t *testing.T) {
	market := make(chan contract.MarketEnvelope, 4)
	exec := make(chan contract.ExecEnvelope, 4)
	account := make(chan contract.AccountEnvelope, 4)

	var mu sync.Mutex
	counts := map[string]int{}

	b := New(market, exec, account)
	done := make(chan struct{})
	go func() {
		b.Run(context.Background(), Handlers{
			OnMarket:  func(contract.MarketEnvelope) { mu.Lock(); counts["m"]++; mu.Unlock() },
			OnExec:    func(contract.ExecEnvelope) { mu.Lock(); counts["e"]++; mu.Unlock() },
			OnAccount: func(contract.AccountEnvelope) { mu.Lock(); counts["a"]++; mu.Unlock() },
		})
		close(done)
	}()

	market <- contract.NewMarketEnvelope(contract.TradeEvent{Trade: model.TradeTick{InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT"}}})
	exec <- contract.NewExecEnvelope(contract.OrderEvent{Order: model.Order{Request: model.OrderRequest{ClientID: "c1"}}})
	exec <- contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{ClientID: "c1", VenueOrderID: "v1"}})
	account <- contract.NewAccountEnvelope(contract.BalanceEvent{Balance: model.AccountBalance{Currency: "USDT"}})

	// Closing all channels should make Run return.
	close(market)
	close(exec)
	close(account)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after all channels closed")
	}

	mu.Lock()
	defer mu.Unlock()
	if counts["m"] != 1 || counts["e"] != 2 || counts["a"] != 1 {
		t.Fatalf("counts=%v, want m=1 e=2 a=1", counts)
	}
}

// TestContextCancel verifies Run stops promptly on context cancel even with
// open channels.
func TestContextCancel(t *testing.T) {
	exec := make(chan contract.ExecEnvelope) // never closed
	b := New(nil, exec, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx, Handlers{}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return on context cancel")
	}
}

// TestNilHandlersAndChannels verifies a partial node (only exec) works and nil
// handlers are tolerated.
func TestNilHandlersAndChannels(t *testing.T) {
	exec := make(chan contract.ExecEnvelope, 1)
	b := New(nil, exec, nil)
	done := make(chan struct{})
	go func() { b.Run(context.Background(), Handlers{}); close(done) }()
	exec <- contract.NewExecEnvelope(contract.OrderEvent{Order: model.Order{Request: model.OrderRequest{ClientID: "c1"}}})
	close(exec)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}
