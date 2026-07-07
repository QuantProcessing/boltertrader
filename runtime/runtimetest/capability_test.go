package runtimetest

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestFakePerpCapabilitySuite(t *testing.T) {
	id := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	market := NewFakeMarket()
	exec := NewFakeExec()
	account := NewFakeAccount()
	req := model.OrderRequest{
		InstrumentID: id,
		ClientID:     "fake-capability",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100),
	}

	contracttest.RunPerpCapabilitySuite(t, contracttest.PerpCapabilitySuite{
		Venue: "FAKE",
		Market: contracttest.MarketCapabilities{
			OrderBook: contracttest.CapabilityProbe{Support: contracttest.Unsupported("fake market is push-only"), Probe: func(ctx context.Context) error {
				_, err := market.OrderBook(ctx, id, 5)
				return err
			}},
			Bars: contracttest.CapabilityProbe{Support: contracttest.Unsupported("fake market is push-only"), Probe: func(ctx context.Context) error {
				_, err := market.Bars(ctx, id, "1m", 10)
				return err
			}},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return market.SubscribeTrades(ctx, id)
			}},
			Reconnect: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return market.Reconnect(ctx)
			}},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := exec.Submit(ctx, req)
				return err
			}},
			Cancel: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return exec.Cancel(ctx, id, "v1")
			}},
			CancelAll: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return exec.CancelAll(ctx, id)
			}},
			Modify: contracttest.CapabilityProbe{Support: contracttest.Unsupported("fake execution does not model amend"), Probe: func(ctx context.Context) error {
				_, err := exec.Modify(ctx, id, "v1", decimal.NewFromInt(1), decimal.NewFromInt(1))
				return err
			}},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := exec.OpenOrders(ctx, id)
				return err
			}},
			MassStatus: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := exec.GenerateExecutionMassStatus(ctx, model.MassStatusQuery{})
				return err
			}},
		},
		Account: contracttest.AccountCapabilities{
			Balances: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := account.Balances(ctx)
				return err
			}},
			Positions: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := account.Positions(ctx)
				return err
			}},
			SetLeverage: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return account.SetLeverage(ctx, id, decimal.NewFromInt(2))
			}},
			SetCrossMargin: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return account.SetMarginMode(ctx, id, "cross")
			}},
		},
	})
}

func TestFakeExecCapabilitiesMatchImplementedReports(t *testing.T) {
	caps := NewFakeExec().Capabilities().Reports
	if caps.FillHistory || caps.PositionReports || caps.AccountBalanceSnapshots || caps.OrderHistory {
		t.Fatalf("fake exec reports=%+v, want only implemented execution reports advertised", caps)
	}
	if !caps.SingleOrderStatus || !caps.OpenOrders {
		t.Fatalf("fake exec reports=%+v, want single-order and open-order status support", caps)
	}
}

func TestFakeAccountDeclaresAccountStateOnlyWhenConfigured(t *testing.T) {
	account := NewFakeAccount()
	caps := account.Capabilities()
	if caps.Reports.AccountStateSnapshots || caps.Streaming.AccountState {
		t.Fatalf("unconfigured fake account should not declare account state support: %+v", caps)
	}

	ts := time.Unix(1, 0)
	account.SetAccountStateSnapshot(model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.NewFromInt(1),
			Free:     decimal.NewFromInt(1),
		}},
		ModeInfo: model.AccountModeInfo{
			Venue:        "BINANCE",
			AccountID:    model.AccountIDBinanceDefault,
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   ts,
			Source:       "test",
		},
		TsEvent: ts,
	})
	caps = account.Capabilities()
	if !caps.Reports.AccountStateSnapshots || !caps.Streaming.AccountState {
		t.Fatalf("configured fake account should declare account state support: %+v", caps)
	}
}

func TestFakeVenueEmitsTestSourcedEnvelopes(t *testing.T) {
	exec := NewFakeExec()
	exec.EmitReject("client-1", "rejected")
	execEnv := <-exec.Events()
	if execEnv.Source != contract.SourceTest || !execEnv.Flags.Has(contract.EventFlagSynthetic) {
		t.Fatalf("exec meta source=%s flags=%b, want test synthetic", execEnv.Source, execEnv.Flags)
	}

	account := NewFakeAccount()
	account.EmitBalance(model.AccountBalance{Currency: "USDT"})
	accountEnv := <-account.Events()
	if accountEnv.Source != contract.SourceTest || !accountEnv.Flags.Has(contract.EventFlagSynthetic) {
		t.Fatalf("account meta source=%s flags=%b, want test synthetic", accountEnv.Source, accountEnv.Flags)
	}

	market := NewFakeMarket()
	market.EmitTrade(model.TradeTick{InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}})
	marketEnv := <-market.Events()
	if marketEnv.Source != contract.SourceTest || !marketEnv.Flags.Has(contract.EventFlagSynthetic) {
		t.Fatalf("market meta source=%s flags=%b, want test synthetic", marketEnv.Source, marketEnv.Flags)
	}
}
