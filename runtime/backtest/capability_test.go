package backtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
)

func TestPerpCapabilitySuite(t *testing.T) {
	id := model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	newVenue := func() *backtest.Venue {
		return backtest.NewVenue(clock.NewSimulatedClock(time.Unix(0, 0)), backtest.Config{})
	}
	orderReq := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			InstrumentID: id,
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("100"),
		}
	}

	contracttest.RunPerpCapabilitySuite(t, contracttest.PerpCapabilitySuite{
		Venue: "BACKTEST",
		Market: contracttest.MarketCapabilities{
			OrderBook: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest is trade/funding replay; no book snapshot"), Probe: func(ctx context.Context) error {
				_, err := newVenue().Market().OrderBook(ctx, id, 5)
				return err
			}},
			Bars: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest consumes replay events; it does not query historical bars"), Probe: func(ctx context.Context) error {
				_, err := newVenue().Market().Bars(ctx, id, "1m", 10)
				return err
			}},
			SubscribeBook: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest emits trades/funding, not book events"), Probe: func(ctx context.Context) error {
				return newVenue().Market().SubscribeBook(ctx, id)
			}},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest emits trades/funding, not quote events"), Probe: func(ctx context.Context) error {
				return newVenue().Market().SubscribeQuotes(ctx, id)
			}},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return newVenue().Market().SubscribeTrades(ctx, id)
			}},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := newVenue().Execution().Submit(ctx, orderReq("submit"))
				return err
			}},
			Cancel: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				order, err := venue.Execution().Submit(ctx, orderReq("cancel"))
				if err != nil {
					return err
				}
				return venue.Execution().Cancel(ctx, id, order.VenueOrderID)
			}},
			CancelAll: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				if _, err := venue.Execution().Submit(ctx, orderReq("cancel-all")); err != nil {
					return err
				}
				return venue.Execution().CancelAll(ctx, id)
			}},
			Modify: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				order, err := venue.Execution().Submit(ctx, orderReq("modify"))
				if err != nil {
					return err
				}
				_, err = venue.Execution().Modify(ctx, id, order.VenueOrderID, d("101"), d("2"))
				return err
			}},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				if _, err := venue.Execution().Submit(ctx, orderReq("open-orders")); err != nil {
					return err
				}
				_, err := venue.Execution().OpenOrders(ctx, id)
				return err
			}},
			OrderReports: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				if _, err := venue.Execution().Submit(ctx, orderReq("order-reports")); err != nil {
					return err
				}
				_, err := venue.Execution().OrderReports(ctx)
				return err
			}},
		},
		Account: contracttest.AccountCapabilities{
			Balances: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := newVenue().Account().Balances(ctx)
				return err
			}},
			Positions: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := newVenue().Account().Positions(ctx)
				return err
			}},
			SetLeverage: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return newVenue().Account().SetLeverage(ctx, id, d("2"))
			}},
			SetCrossMargin: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return newVenue().Account().SetMarginMode(ctx, id, "cross")
			}},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest currently models cross margin only"), Probe: func(ctx context.Context) error {
				return newVenue().Account().SetMarginMode(ctx, id, "isolated")
			}},
		},
	})
}

func TestSpotCapabilitySuite(t *testing.T) {
	id := model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	newVenue := func() *backtest.Venue {
		return backtest.NewVenue(clock.NewSimulatedClock(time.Unix(0, 0)), backtest.Config{
			StartBalance: model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
			Instruments: []*model.Instrument{{
				ID:    id,
				Base:  "BTC",
				Quote: "USDT",
			}},
		})
	}
	orderReq := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			InstrumentID: id,
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("100"),
		}
	}

	contracttest.RunSpotCapabilitySuite(t, contracttest.SpotCapabilitySuite{
		Venue: "BACKTEST_SPOT",
		Market: contracttest.MarketCapabilities{
			OrderBook: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest is trade replay; no book snapshot"), Probe: func(ctx context.Context) error {
				_, err := newVenue().Market().OrderBook(ctx, id, 5)
				return err
			}},
			Bars: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest consumes replay events; it does not query historical bars"), Probe: func(ctx context.Context) error {
				_, err := newVenue().Market().Bars(ctx, id, "1m", 10)
				return err
			}},
			SubscribeBook: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest emits trades, not book events"), Probe: func(ctx context.Context) error {
				return newVenue().Market().SubscribeBook(ctx, id)
			}},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.Unsupported("backtest emits trades, not quote events"), Probe: func(ctx context.Context) error {
				return newVenue().Market().SubscribeQuotes(ctx, id)
			}},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				return newVenue().Market().SubscribeTrades(ctx, id)
			}},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := newVenue().Execution().Submit(ctx, orderReq("spot-submit"))
				return err
			}},
			Cancel: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				order, err := venue.Execution().Submit(ctx, orderReq("spot-cancel"))
				if err != nil {
					return err
				}
				return venue.Execution().Cancel(ctx, id, order.VenueOrderID)
			}},
			CancelAll: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				if _, err := venue.Execution().Submit(ctx, orderReq("spot-cancel-all")); err != nil {
					return err
				}
				return venue.Execution().CancelAll(ctx, id)
			}},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				if _, err := venue.Execution().Submit(ctx, orderReq("spot-open-orders")); err != nil {
					return err
				}
				_, err := venue.Execution().OpenOrders(ctx, id)
				return err
			}},
			OrderReports: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				venue := newVenue()
				if _, err := venue.Execution().Submit(ctx, orderReq("spot-order-reports")); err != nil {
					return err
				}
				_, err := venue.Execution().OrderReports(ctx)
				return err
			}},
		},
		Account: contracttest.AccountCapabilities{
			Balances: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := newVenue().Account().Balances(ctx)
				return err
			}},
			Positions: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				_, err := newVenue().Account().Positions(ctx)
				return err
			}},
			SetLeverage: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash accounts do not support leverage"), Probe: func(ctx context.Context) error {
				return newVenue().Account().SetLeverage(ctx, id, d("2"))
			}},
			SetCrossMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash accounts do not support margin mode"), Probe: func(ctx context.Context) error {
				return newVenue().Account().SetMarginMode(ctx, id, "cross")
			}},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash accounts do not support margin mode"), Probe: func(ctx context.Context) error {
				return newVenue().Account().SetMarginMode(ctx, id, "isolated")
			}},
		},
	})
}
