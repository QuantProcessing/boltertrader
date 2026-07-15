package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

type multiplierMarket struct {
	*runtimetest.FakeMarket
	provider model.InstrumentProvider
}

func TestWithRiskDefaultsToMarketInstrumentProvider(t *testing.T) {
	id := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	provider := nodeInstrumentProvider{inst: &model.Instrument{
		ID: id, ContractMultiplier: decimal.RequireFromString("0.0001"),
	}}
	market := &multiplierMarket{FakeMarket: runtimetest.NewFakeMarket(), provider: provider}
	execution := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Market: market, Execution: execution}, nil, "multiplier-risk")
	riskEngine := risk.New(risk.Limits{MaxOrderNotional: decimal.NewFromInt(20)}, node.Cache).WithInstrumentProvider(provider)
	runtime.WithRisk(riskEngine, nil)(node)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)
	if _, err := node.Exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: id,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100000),
	}); err != nil {
		t.Fatalf("multiplier-aware notional 10 should pass max 20: %v", err)
	}
}

func (m *multiplierMarket) InstrumentProvider() model.InstrumentProvider { return m.provider }

func TestNewNodeBindsMarketInstrumentMultiplierIntoPortfolio(t *testing.T) {
	id := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	provider := nodeInstrumentProvider{inst: &model.Instrument{
		ID:                 id,
		ContractMultiplier: decimal.RequireFromString("0.0001"),
	}}
	market := &multiplierMarket{FakeMarket: runtimetest.NewFakeMarket(), provider: provider}
	node := runtime.NewNode(runtime.Clients{Market: market}, nil, "multiplier")

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	state := model.AccountState{
		AccountID:    "FAKE:perp",
		Venue:        "FAKE",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Reported:     true,
		EventID:      model.AccountStateEventID("FAKE", "FAKE:perp", now),
		TsEvent:      now,
		TsInit:       now,
	}
	if err := node.Cache.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	node.Cache.UpsertPosition(model.Position{
		AccountID:    "FAKE:perp",
		InstrumentID: id,
		Side:         enums.PosNet,
		Quantity:     decimal.NewFromInt(1),
		MarkPrice:    decimal.NewFromInt(100000),
	})

	exposure, ok := node.Portfolio.NetExposure("FAKE:perp")
	if !ok {
		t.Fatal("portfolio account lookup failed")
	}
	if got := exposure[id]; !got.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("node portfolio exposure=%s, want multiplier-aware 10", got)
	}
}

func TestWithRiskBindsExplicitInstrumentProviderIntoPortfolio(t *testing.T) {
	id := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	provider := nodeInstrumentProvider{inst: &model.Instrument{
		ID:                 id,
		ContractMultiplier: decimal.RequireFromString("0.01"),
	}}
	node := runtime.NewNode(runtime.Clients{Execution: runtimetest.NewFakeExec()}, nil, "explicit-provider")
	runtime.WithRisk(risk.New(risk.Limits{}, node.Cache).WithInstrumentProvider(provider), provider)(node)

	node.Portfolio.OnFill(model.Fill{
		AccountID: "FAKE:perp", InstrumentID: id, Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
	}, enums.PosNet)
	node.Portfolio.OnFill(model.Fill{
		AccountID: "FAKE:perp", InstrumentID: id, Side: enums.SideSell,
		Price: decimal.NewFromInt(110), Quantity: decimal.NewFromInt(1),
	}, enums.PosNet)

	if got := node.Portfolio.RealizedPnLForAccount("FAKE:perp"); !got.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("execution-only portfolio realized=%s, want multiplier-aware 0.1", got)
	}
}
