package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

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
	balances          []model.AccountBalance
	positions         []model.Position
	positionErr       error
	accountState      model.AccountState
	hasAccountState   bool
	balanceCalls      int
	accountStateCalls int
}

func (s *snapshotAccount) Capabilities() contract.Capabilities {
	caps := contract.Capabilities{Venue: "T"}
	if s.hasAccountState {
		caps.Reports.AccountStateSnapshots = true
	}
	return caps
}
func (s *snapshotAccount) Balances(context.Context) ([]model.AccountBalance, error) {
	s.balanceCalls++
	return s.balances, nil
}
func (s *snapshotAccount) AccountState(context.Context) (model.AccountState, error) {
	s.accountStateCalls++
	return s.accountState, nil
}
func (s *snapshotAccount) Positions(context.Context) ([]model.Position, error) {
	if s.positionErr != nil {
		return nil, s.positionErr
	}
	return s.positions, nil
}
func (s *snapshotAccount) SetLeverage(context.Context, model.InstrumentID, decimal.Decimal) error {
	return nil
}
func (s *snapshotAccount) SetMarginMode(context.Context, model.InstrumentID, string) error {
	return nil
}
func (s *snapshotAccount) Events() <-chan contract.AccountEnvelope { return nil }
func (s *snapshotAccount) Close() error                            { return nil }

// snapshotExec is a minimal ExecutionClient returning a canned venue-wide
// open-order snapshot for reconciliation.
type snapshotExec struct {
	reports []model.Order
	mass    *model.ExecutionMassStatus
	queries []model.MassStatusQuery
}

func (s *snapshotExec) Capabilities() contract.Capabilities { return contract.Capabilities{Venue: "T"} }
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
func (s *snapshotExec) GenerateOrderStatusReports(context.Context, model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	out := make([]model.OrderStatusReport, 0, len(s.reports))
	for _, o := range s.reports {
		out = append(out, model.OrderStatusReport{Venue: o.Request.InstrumentID.Venue, Order: o})
	}
	return out, nil
}
func (s *snapshotExec) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	reports, err := s.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
		InstrumentID: query.InstrumentID,
		ClientID:     query.ClientID,
		VenueOrderID: query.VenueOrderID,
	})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	return &reports[0], nil
}
func (s *snapshotExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, contract.ErrNotSupported
}
func (s *snapshotExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, contract.ErrNotSupported
}
func (s *snapshotExec) GenerateExecutionMassStatus(_ context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	s.queries = append(s.queries, query)
	if s.mass != nil {
		mass := s.mass.Clone()
		return &mass, nil
	}
	mass := model.NewExecutionMassStatus("T", query.AccountID, time.Time{})
	for _, o := range s.reports {
		if err := mass.AddOrderReport(model.OrderStatusReport{Venue: o.Request.InstrumentID.Venue, AccountID: query.AccountID, Order: o}); err != nil {
			return nil, err
		}
	}
	return mass, nil
}
func (s *snapshotExec) Events() <-chan contract.ExecEnvelope { return nil }
func (s *snapshotExec) Close() error                         { return nil }

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

func TestReconcileOrdersClosesOnlyScopedAccountOrders(t *testing.T) {
	c := cache.New()
	acctAOpen := order("acct-a-open", btc, "1", enums.StatusNew)
	acctAOpen.Request.AccountID = "acct-a"
	acctBOpen := order("acct-b-open", btc, "1", enums.StatusNew)
	acctBOpen.Request.AccountID = "acct-b"
	c.UpsertOrder(acctAOpen)
	c.UpsertOrder(acctBOpen)

	mass := model.NewExecutionMassStatus("T", "acct-a", time.Unix(100, 0))
	r := New(nil, &snapshotExec{mass: mass}, c).WithAccountID("acct-a")
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersClosedUnknown != 1 {
		t.Fatalf("report=%+v, want one scoped close", rep)
	}
	if got, ok := c.Order("acct-a-open"); !ok || got.Status != enums.StatusUnknown {
		t.Fatalf("acct-a order=%+v ok=%v, want unknown", got, ok)
	}
	if got, ok := c.Order("acct-b-open"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("acct-b order=%+v ok=%v, want untouched NEW", got, ok)
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

func TestReconcileAccountSnapshotsAreScopedByAccountID(t *testing.T) {
	c := cache.New()
	c.UpsertPosition(model.Position{AccountID: "acct-a", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	c.UpsertPosition(model.Position{AccountID: "acct-a", InstrumentID: eth, Side: enums.PosNet, Quantity: d("3")})
	c.UpsertPosition(model.Position{AccountID: "acct-b", InstrumentID: eth, Side: enums.PosNet, Quantity: d("7")})

	acct := &snapshotAccount{
		balances: []model.AccountBalance{{Currency: "USDT", Total: d("1000"), Free: d("900")}},
		positions: []model.Position{
			{InstrumentID: btc, Side: enums.PosNet, Quantity: d("2.5"), EntryPrice: d("60000")},
		},
	}

	r := New(acct, nil, c).WithAccountID("acct-a")
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.BalancesUpdated != 1 || rep.PositionsUpdated != 1 || rep.PositionsCleared != 1 {
		t.Fatalf("report=%+v, want balances=1 positions=1 cleared=1", rep)
	}
	if p, ok := c.PositionForAccount("acct-a", btc, enums.PosNet); !ok || !p.Quantity.Equal(d("2.5")) {
		t.Fatalf("acct-a BTC position=%+v ok=%v, want qty 2.5", p, ok)
	}
	if _, ok := c.PositionForAccount("acct-a", eth, enums.PosNet); ok {
		t.Fatal("acct-a stale ETH position should be cleared")
	}
	if p, ok := c.PositionForAccount("acct-b", eth, enums.PosNet); !ok || !p.Quantity.Equal(d("7")) {
		t.Fatalf("acct-b ETH position=%+v ok=%v, want untouched qty 7", p, ok)
	}
	if b, ok := c.BalanceForAccount("acct-a", "USDT"); !ok || !b.Free.Equal(d("900")) {
		t.Fatalf("acct-a balance=%+v ok=%v, want free 900", b, ok)
	}
}

func TestReconcilePrefersAccountStateWhenCapabilityDeclared(t *testing.T) {
	c := cache.New()
	ts := time.Unix(20, 0)
	acct := &snapshotAccount{
		hasAccountState: true,
		accountState: model.AccountState{
			AccountID: model.AccountIDBinanceDefault,
			Venue:     "BINANCE",
			Type:      model.AccountMargin,
			Balances: []model.AccountBalance{{
				Currency: "USDT",
				Total:    d("1000"),
				Free:     d("950"),
			}},
			ModeInfo: model.AccountModeInfo{
				Venue:        "BINANCE",
				AccountID:    model.AccountIDBinanceDefault,
				AccountMode:  "USD-M",
				ProductScope: []enums.InstrumentKind{enums.KindPerp},
				Verified:     true,
				VerifiedAt:   ts,
				Source:       "test",
			},
			Reported: true,
			TsEvent:  ts,
		},
		balances: []model.AccountBalance{{Currency: "USDT", Total: d("1"), Available: d("1")}},
	}

	r := New(acct, nil, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.AccountStatesApplied != 1 || rep.BalancesUpdated != 1 {
		t.Fatalf("report=%+v, want one account state and one balance update", rep)
	}
	if acct.accountStateCalls != 1 || acct.balanceCalls != 0 {
		t.Fatalf("calls: accountState=%d balances=%d, want 1/0", acct.accountStateCalls, acct.balanceCalls)
	}
	cached, ok := c.Account(model.AccountIDBinanceDefault)
	if !ok {
		t.Fatal("account state not applied to cache")
	}
	if cached.Freshness().LastReconciledAt.IsZero() {
		t.Fatal("account state reconciliation should mark LastReconciledAt")
	}
	if bal, ok := c.Balance("USDT"); !ok || !bal.Free.Equal(d("950")) {
		t.Fatalf("compat balance=%+v ok=%v, want free 950", bal, ok)
	}
}

func TestReconcileDoesNotMarkAccountReconciledWhenPositionsFail(t *testing.T) {
	c := cache.New()
	ts := time.Unix(20, 0)
	positionErr := errors.New("positions unavailable")
	acct := &snapshotAccount{
		hasAccountState: true,
		positionErr:     positionErr,
		accountState: model.AccountState{
			AccountID: model.AccountIDBinanceDefault,
			Venue:     "BINANCE",
			Type:      model.AccountMargin,
			Balances: []model.AccountBalance{{
				Currency: "USDT",
				Total:    d("1000"),
				Free:     d("950"),
			}},
			ModeInfo: model.AccountModeInfo{
				Venue:        "BINANCE",
				AccountID:    model.AccountIDBinanceDefault,
				ProductScope: []enums.InstrumentKind{enums.KindPerp},
				Verified:     true,
				VerifiedAt:   ts,
				Source:       "test",
			},
			Reported: true,
			TsEvent:  ts,
		},
	}

	r := New(acct, nil, c)
	_, err := r.Run(context.Background())
	if !errors.Is(err, positionErr) {
		t.Fatalf("run err=%v, want positions error", err)
	}
	cached, ok := c.Account(model.AccountIDBinanceDefault)
	if !ok {
		t.Fatal("account state should be applied before positions fail")
	}
	if !cached.Freshness().LastReconciledAt.IsZero() {
		t.Fatalf("account should not be marked reconciled after positions fail: %+v", cached.Freshness())
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
