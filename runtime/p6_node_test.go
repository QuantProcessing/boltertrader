package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

// TestRiskGateBlocksSubmit verifies the pre-trade risk gate rejects an
// over-limit order before it reaches the venue, and the cache holds no order.
func TestRiskGateBlocksSubmit(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()

	node := runtime.NewNode(runtime.Clients{Execution: fexec}, clk, "rk")
	riskEng := risk.New(risk.Limits{MaxOrderQty: decimal.RequireFromString("5")}, node.Cache)
	// risk.New needs node.Cache, so apply the option after construction.
	runtime.WithRisk(riskEng, nil)(node)

	ctx := context.Background()
	go node.Run(ctx)

	_, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit,
		Quantity: decimal.RequireFromString("10"), Price: decimal.RequireFromString("100"),
	})
	if !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("over-limit order should be risk-rejected, got %v", err)
	}
	if all := node.Cache.Orders(); len(all) != 0 {
		t.Fatalf("rejected order must not be cached, found %d", len(all))
	}

	// A within-limit order passes.
	if _, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit,
		Quantity: decimal.RequireFromString("2"), Price: decimal.RequireFromString("100"),
	}); err != nil {
		t.Fatalf("within-limit order should pass: %v", err)
	}
}

// reconnAccount is a FakeAccount that also implements contract.Reconnectable and
// returns a position snapshot, to exercise Reconnect -> Resync.
type reconnAccount struct {
	*runtimetest.FakeAccount
	reconnects int
	positions  []model.Position
}

func (r *reconnAccount) Connected() bool { return r.reconnects > 0 }
func (r *reconnAccount) Reconnect(ctx context.Context) error {
	r.reconnects++
	return nil
}
func (r *reconnAccount) Positions(ctx context.Context) ([]model.Position, error) {
	return r.positions, nil
}

// TestReconnectTriggersResync verifies node.Reconnect calls the adapter's
// Reconnect and then reconciles the cache from the snapshot.
func TestReconnectTriggersResync(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	acct := &reconnAccount{
		FakeAccount: runtimetest.NewFakeAccount(),
		positions: []model.Position{
			{InstrumentID: inst, Side: enums.PosNet, Quantity: decimal.RequireFromString("1.5"), EntryPrice: decimal.RequireFromString("100")},
		},
	}
	node := runtime.NewNode(runtime.Clients{Account: acct}, clk, "rc")

	rep, err := node.Reconnect(context.Background())
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if acct.reconnects != 1 {
		t.Fatalf("reconnects=%d, want 1", acct.reconnects)
	}
	if rep.PositionsUpdated != 1 {
		t.Fatalf("report=%+v, want PositionsUpdated=1", rep)
	}
	if p, ok := node.Cache.Position(inst, enums.PosNet); !ok || !p.Quantity.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("position not synced from reconnect: ok=%v", ok)
	}
}
