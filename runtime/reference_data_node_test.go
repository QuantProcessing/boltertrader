package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

type referenceStrategy struct {
	strategy.Base
	seen chan referenceObservation
}

type referenceObservation struct {
	snapshot model.DerivativeReferenceSnapshot
	meta     contract.EventMeta
	oi       model.OpenInterestSnapshot
	err      error
	cached   bool
}

func (s *referenceStrategy) OnDerivativeReference(c *strategy.Context, snapshot model.DerivativeReferenceSnapshot) {
	cached, ok := c.Cache.DerivativeReference(snapshot.InstrumentID)
	oi, err := c.OpenInterest(c.Ctx, snapshot.InstrumentID)
	s.seen <- referenceObservation{snapshot: cached, meta: c.CurrentEventMeta(), oi: oi, err: err, cached: ok}
}

type directStrategyNoReference struct {
	mu sync.Mutex
}

func (s *directStrategyNoReference) OnStart(*strategy.Context)                  {}
func (s *directStrategyNoReference) OnBar(*strategy.Context, model.Bar)         {}
func (s *directStrategyNoReference) OnQuote(*strategy.Context, model.QuoteTick) {}
func (s *directStrategyNoReference) OnTrade(*strategy.Context, model.TradeTick) {}
func (s *directStrategyNoReference) OnFill(*strategy.Context, model.Fill)       {}
func (s *directStrategyNoReference) OnStop(*strategy.Context)                   {}

func TestReferenceDataEventUpdatesCacheAndOptionalStrategy(t *testing.T) {
	fmarket := runtimetest.NewFakeMarket()
	strat := &referenceStrategy{seen: make(chan referenceObservation, 1)}
	node := runtime.NewNode(runtime.Clients{Market: fmarket}, nil, "ref", runtime.WithStrategy(strat))

	if err := fmarket.SubscribeReference(context.Background(), inst); err != nil {
		t.Fatalf("subscribe reference: %v", err)
	}
	oi := model.OpenInterestSnapshot{
		InstrumentID: inst,
		OpenInterest: decimal.RequireFromString("12345"),
		Unit:         "contracts",
		Fields:       model.OpenInterestHasQuantity.With(model.OpenInterestHasUnit),
	}
	fmarket.SetOpenInterestSnapshot(oi)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	ts := time.Unix(300, 0)
	fmarket.EmitDerivativeReference(model.DerivativeReferenceSnapshot{
		InstrumentID: inst,
		MarkPrice:    decimal.RequireFromString("64100"),
		Timestamp:    ts,
		ReceivedAt:   ts.Add(time.Millisecond),
		Fields:       model.ReferenceHasMarkPrice,
	})

	select {
	case got := <-strat.seen:
		if !got.cached {
			t.Fatal("strategy callback ran before cache update")
		}
		if !got.snapshot.MarkPrice.Equal(decimal.RequireFromString("64100")) {
			t.Fatalf("cached mark=%s", got.snapshot.MarkPrice)
		}
		if got.meta.EventID == "" || got.meta.Source != contract.SourceTest {
			t.Fatalf("strategy metadata=%+v", got.meta)
		}
		if got.err != nil {
			t.Fatalf("strategy OpenInterest: %v", got.err)
		}
		if !got.oi.OpenInterest.Equal(decimal.RequireFromString("12345")) {
			t.Fatalf("oi=%s", got.oi.OpenInterest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("strategy did not receive derivative reference callback")
	}

	nodeOI, err := node.OpenInterest(context.Background(), inst)
	if err != nil {
		t.Fatalf("node OpenInterest: %v", err)
	}
	if !nodeOI.OpenInterest.Equal(decimal.RequireFromString("12345")) {
		t.Fatalf("node OI=%s", nodeOI.OpenInterest)
	}
}

func TestOpenInterestUnsupportedWhenMarketDoesNotImplementClient(t *testing.T) {
	node := runtime.NewNode(runtime.Clients{}, nil, "ref")
	if _, err := node.OpenInterest(context.Background(), inst); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("OpenInterest err=%v, want ErrNotSupported", err)
	}

	ctx := &strategy.Context{Ctx: context.Background()}
	if _, err := ctx.OpenInterest(context.Background(), inst); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("strategy OpenInterest err=%v, want ErrNotSupported", err)
	}
}

func TestDirectStrategyDoesNotNeedDerivativeReferenceMethod(t *testing.T) {
	var _ strategy.Strategy = (*directStrategyNoReference)(nil)
	if _, ok := any(&directStrategyNoReference{}).(strategy.DerivativeReferenceHandler); ok {
		t.Fatal("direct strategy unexpectedly implements optional reference handler")
	}
}
