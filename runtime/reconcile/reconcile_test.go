package reconcile

import (
	"context"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var (
	btc = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	eth = model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
)

var spotBTC = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindSpot}

// snapshotAccount is a minimal AccountClient returning canned snapshots.
type snapshotAccount struct {
	balances  []model.AccountBalance
	positions []model.Position
}

func (s *snapshotAccount) Balances(context.Context) ([]model.AccountBalance, error) {
	return s.balances, nil
}
func (s *snapshotAccount) Positions(context.Context) ([]model.Position, error) {
	return s.positions, nil
}
func (s *snapshotAccount) SetLeverage(context.Context, model.InstrumentID, decimal.Decimal) error {
	return nil
}
func (s *snapshotAccount) SetMarginMode(context.Context, model.InstrumentID, string) error {
	return nil
}
func (s *snapshotAccount) Events() <-chan contract.AccountEvent { return nil }
func (s *snapshotAccount) Close() error                         { return nil }

// snapshotExec is a minimal ExecutionClient returning a canned venue-wide
// open-order snapshot for reconciliation.
type snapshotExec struct {
	reports []model.Order
}

func (s *snapshotExec) Submit(context.Context, model.OrderRequest) (*model.Order, error) {
	return nil, nil
}
func (s *snapshotExec) Cancel(context.Context, model.InstrumentID, string) error { return nil }
func (s *snapshotExec) CancelAll(context.Context, model.InstrumentID) error      { return nil }
func (s *snapshotExec) Modify(context.Context, model.InstrumentID, string, decimal.Decimal, decimal.Decimal) (*model.Order, error) {
	return nil, nil
}
func (s *snapshotExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return s.reports, nil
}
func (s *snapshotExec) OrderReports(context.Context) ([]model.Order, error) { return s.reports, nil }
func (s *snapshotExec) Events() <-chan contract.ExecEvent                   { return nil }
func (s *snapshotExec) Close() error                                        { return nil }

// order builds a minimal open order with a client id and instrument.
func order(clientID string, id model.InstrumentID, qty string, status enums.OrderStatus) model.Order {
	return model.Order{
		Request:      model.OrderRequest{ClientID: clientID, InstrumentID: id, Quantity: d(qty)},
		VenueOrderID: "v-" + clientID,
		Status:       status,
	}
}

// TestReconcileOrders: the cache holds two open orders. The venue reports one of
// them (refreshed), a brand-new external order, but NOT the second cached order.
// A missing cached order is no longer resting, but this pass must not claim it
// was canceled or filled: the close reason is unknown until trade reconciliation
// can prove it.
func TestReconcileOrders(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(order("a", btc, "1", enums.StatusNew)) // still open at venue
	c.UpsertOrder(order("b", eth, "2", enums.StatusNew)) // gone at venue -> closed unknown

	exec := &snapshotExec{reports: []model.Order{
		order("a", btc, "1", enums.StatusPartiallyFilled), // known -> updated
		order("c", btc, "5", enums.StatusNew),             // external -> adopted
	}}

	r := New(nil, exec, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersUpdated != 1 || rep.OrdersExternal != 1 || rep.OrdersClosedUnknown != 1 || rep.OrdersCleared != 0 {
		t.Fatalf("report=%+v, want updated=1 external=1 closedUnknown=1 cleared=0", rep)
	}

	// Order "a" refreshed to venue truth (PartiallyFilled).
	if o, ok := c.Order("a"); !ok || o.Status != enums.StatusPartiallyFilled {
		t.Fatalf("order a not refreshed: ok=%v status=%v", ok, o.Status)
	}
	// External order "c" adopted into the cache as open.
	if o, ok := c.Order("c"); !ok || o.Status != enums.StatusNew {
		t.Fatalf("external order c not adopted: ok=%v status=%v", ok, o.Status)
	}
	// Order "b" is closed locally without inventing a venue close reason.
	if o, ok := c.Order("b"); !ok || o.Status != enums.StatusUnknown {
		t.Fatalf("order b not closed unknown: ok=%v status=%v", ok, o.Status)
	}
	// And "b" must no longer appear among open orders.
	for _, o := range c.OpenOrders() {
		if o.Request.ClientID == "b" {
			t.Fatal("order b still open after reconcile")
		}
	}
}

// TestReconcileOrdersNilExec: with no execution client, order reconciliation is
// skipped and the cache's orders are untouched.
func TestReconcileOrdersNilExec(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(order("a", btc, "1", enums.StatusNew))

	r := New(nil, nil, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersUpdated+rep.OrdersExternal+rep.OrdersCleared+rep.OrdersClosedUnknown != 0 {
		t.Fatalf("expected no order changes, got %+v", rep)
	}
	if o, ok := c.Order("a"); !ok || o.Status != enums.StatusNew {
		t.Fatalf("order a should be untouched: ok=%v status=%v", ok, o.Status)
	}
}

// TestReconcileCorrectsCache: cache holds a stale BTC long and a stale ETH long;
// the venue snapshot reports a different BTC long and no ETH. After Run, BTC is
// corrected and ETH is cleared.
func TestReconcileCorrectsCache(t *testing.T) {
	c := cache.New()
	// Stale local state.
	c.UpsertPosition(model.Position{InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	c.UpsertPosition(model.Position{InstrumentID: eth, Side: enums.PosNet, Quantity: d("3")})

	acct := &snapshotAccount{
		balances: []model.AccountBalance{{Currency: "USDT", Total: d("1000"), Available: d("900")}},
		positions: []model.Position{
			{InstrumentID: btc, Side: enums.PosNet, Quantity: d("2.5"), EntryPrice: d("60000")},
		},
	}

	r := New(acct, nil, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.BalancesUpdated != 1 || rep.PositionsUpdated != 1 || rep.PositionsCleared != 1 {
		t.Fatalf("report=%+v, want balances=1 positions=1 cleared=1", rep)
	}

	// BTC corrected to 2.5.
	if p, ok := c.Position(btc, enums.PosNet); !ok || !p.Quantity.Equal(d("2.5")) {
		t.Fatalf("BTC position not corrected: ok=%v qty=%s", ok, p.Quantity)
	}
	// ETH cleared.
	if _, ok := c.Position(eth, enums.PosNet); ok {
		t.Fatal("stale ETH position should be cleared")
	}
	// Balance applied.
	if b, ok := c.Balance("USDT"); !ok || !b.Total.Equal(d("1000")) {
		t.Fatalf("balance not applied: ok=%v", ok)
	}
}

func TestReconcileSpotBalancesWithEmptyPositionsDoesNotFabricateMarginPosition(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "BTC", Total: d("1"), Available: d("1")})
	c.UpsertPosition(model.Position{InstrumentID: spotBTC, Side: enums.PosNet, Quantity: d("1")})

	acct := &snapshotAccount{
		balances: []model.AccountBalance{
			{Currency: "BTC", Total: d("2"), Available: d("2")},
			{Currency: "USDT", Total: d("800"), Available: d("800")},
		},
		positions: nil,
	}

	r := New(acct, nil, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.BalancesUpdated != 2 || rep.PositionsUpdated != 0 || rep.PositionsCleared != 1 {
		t.Fatalf("report=%+v, want balances=2 positionsUpdated=0 positionsCleared=1", rep)
	}
	if b, ok := c.Balance("BTC"); !ok || !b.Total.Equal(d("2")) || !b.Available.Equal(d("2")) {
		t.Fatalf("BTC balance not reconciled: ok=%v balance=%+v", ok, b)
	}
	if b, ok := c.Balance("USDT"); !ok || !b.Total.Equal(d("800")) || !b.Available.Equal(d("800")) {
		t.Fatalf("USDT balance not reconciled: ok=%v balance=%+v", ok, b)
	}
	if _, ok := c.Position(spotBTC, enums.PosNet); ok {
		t.Fatal("spot inventory must remain balance-sourced; reconcile should not retain synthetic spot position")
	}
}
