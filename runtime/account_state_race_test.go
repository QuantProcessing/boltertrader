package runtime_test

import (
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/portfolio"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/shopspring/decimal"
)

func TestAccountSnapshotConcurrentApplyAndRead(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	inst := &model.Instrument{
		ID:                 id,
		Base:               "BTC",
		Quote:              "USDT",
		Settle:             "USDT",
		ContractMultiplier: decimal.NewFromInt(1),
	}
	if err := c.ApplyAccountStateAt(concurrentAccountState(now, "1000"), now); err != nil {
		t.Fatalf("seed account state: %v", err)
	}
	c.UpsertPosition(model.Position{
		InstrumentID:  id,
		Side:          enums.PosNet,
		Quantity:      decimal.NewFromInt(1),
		EntryPrice:    decimal.NewFromInt(100),
		MarkPrice:     decimal.NewFromInt(101),
		UnrealizedPnL: decimal.NewFromInt(1),
	})

	pf := portfolio.New().WithAccountSource(c)
	engine := risk.New(risk.Limits{}, c).
		WithClock(func() time.Time { return now.Add(time.Second) }).
		RequireAccountState()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				ts := now.Add(time.Duration(offset*250+i) * time.Millisecond)
				if err := c.ApplyAccountStateAt(concurrentAccountState(ts, decimal.NewFromInt(int64(1000+i)).String()), ts); err != nil {
					errs <- err
					return
				}
				c.MarkAccountReconciled("T:perp", ts)
			}
		}(writer)
	}
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if acct, ok := c.Account("T:perp"); ok {
					_ = acct.LastEvent()
					_ = acct.Freshness()
					_ = acct.Balances()
					_ = acct.Margins()
					_, _ = acct.BalanceFree("USDT")
				}
				_, _ = pf.Equity("T:perp")
				_, _ = pf.MarginInitial("T:perp")
				_, _ = pf.MarginMaintenance("T:perp")
				_, _ = pf.NetExposure("T:perp")
				err := engine.Check(model.OrderRequest{
					InstrumentID: id,
					Side:         enums.SideBuy,
					Type:         enums.TypeLimit,
					TIF:          enums.TifGTC,
					Quantity:     decimal.RequireFromString("0.01"),
					Price:        decimal.NewFromInt(100),
					PositionSide: enums.PosNet,
				}, inst)
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent account read/write failed: %v", err)
	}
}

func concurrentAccountState(ts time.Time, free string) model.AccountState {
	freeDec := decimal.RequireFromString(free)
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	return model.AccountState{
		AccountID:    "T:perp",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    freeDec,
			Free:     freeDec,
		}},
		Margins: []model.MarginBalance{{
			Currency:     "USDT",
			InstrumentID: &id,
			Initial:      decimal.NewFromInt(10),
			Maintenance:  decimal.NewFromInt(5),
			UpdatedAt:    ts,
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:perp", ts),
		TsEvent:  ts,
		TsInit:   ts,
	}
}
