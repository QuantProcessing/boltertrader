package runtimetest

import (
	"context"
	"testing"

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
			OrderReports: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := exec.OrderReports(ctx)
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
