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
	accountStates     []model.AccountState
	hasAccountState   bool
	positionReports   bool
	balanceCalls      int
	accountStateCalls int
}

func (s *snapshotAccount) Capabilities() contract.Capabilities {
	caps := contract.Capabilities{Venue: "T"}
	if s.hasAccountState {
		caps.Reports.AccountStateSnapshots = true
	}
	caps.Reports.PositionReports = s.positionReports
	return caps
}
func (s *snapshotAccount) Balances(context.Context) ([]model.AccountBalance, error) {
	s.balanceCalls++
	return s.balances, nil
}
func (s *snapshotAccount) AccountState(context.Context) (model.AccountState, error) {
	s.accountStateCalls++
	if len(s.accountStates) != 0 {
		index := s.accountStateCalls - 1
		if index >= len(s.accountStates) {
			index = len(s.accountStates) - 1
		}
		return s.accountStates[index], nil
	}
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
	reports     []model.Order
	mass        *model.ExecutionMassStatus
	massFn      func(model.MassStatusQuery) *model.ExecutionMassStatus
	massErr     error
	queries     []model.MassStatusQuery
	fillHistory bool
	positions   bool
	massDelay   time.Duration
}

func (s *snapshotExec) Capabilities() contract.Capabilities {
	caps := contract.Capabilities{Venue: "T"}
	caps.Reports.FillHistory = s.fillHistory
	caps.Reports.PositionReports = s.positions
	if s.positions {
		caps.Products = []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true, Account: true}}
	}
	return caps
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
func (s *snapshotExec) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	s.queries = append(s.queries, query)
	if s.massDelay > 0 {
		timer := time.NewTimer(s.massDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if s.massErr != nil {
		return nil, s.massErr
	}
	if s.massFn != nil {
		return s.massFn(query), nil
	}
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
	// Unknown is intentionally non-terminal: the unresolved order remains in the
	// restricted working set until stronger history proves its terminal reason.
	foundUnknown := false
	for _, o := range c.OpenOrders() {
		if o.Request.ClientID == "b" {
			foundUnknown = o.Status == enums.StatusUnknown
		}
	}
	if !foundUnknown {
		t.Fatal("order b missing from unresolved open-order working set")
	}
}

func TestReconcileCompleteOpenSetClosesMissingUnknownButFailedMassDoesNot(t *testing.T) {
	t.Run("complete open set closes missing unknown", func(t *testing.T) {
		c := cache.New()
		c.UpsertOrder(order("missed-cancel", btc, "1", enums.StatusNew))
		mass := model.NewExecutionMassStatus("T", "", time.Unix(100, 0))
		rep, err := New(nil, &snapshotExec{mass: mass}, c).Run(context.Background())
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if rep.Partial || rep.OrdersClosedUnknown != 1 {
			t.Fatalf("report=%+v, want non-partial close-unknown", rep)
		}
		if got, ok := c.Order("missed-cancel"); !ok || got.Status != enums.StatusUnknown {
			t.Fatalf("order=%+v ok=%v, want StatusUnknown", got, ok)
		}
	})

	t.Run("failed mass status leaves missing order open", func(t *testing.T) {
		c := cache.New()
		c.UpsertOrder(order("unknown-scope", btc, "1", enums.StatusNew))
		fail := errors.New("instrument open-order query failed")
		if _, err := New(nil, &snapshotExec{massErr: fail}, c).Run(context.Background()); !errors.Is(err, fail) {
			t.Fatalf("run err=%v, want %v", err, fail)
		}
		if got, ok := c.Order("unknown-scope"); !ok || got.Status != enums.StatusNew {
			t.Fatalf("order=%+v ok=%v, want still NEW after failed mass status", got, ok)
		}
	})
}

func TestReconcileOlderOrderSnapshotDoesNotRegressNewerStreamState(t *testing.T) {
	c := cache.New()
	newerAt := time.Unix(50, 0)
	olderAt := newerAt.Add(-time.Second)
	newer := order("partial", btc, "2", enums.StatusPartiallyFilled)
	newer.Request.Price = d("101")
	newer.FilledQty = d("1")
	newer.AvgFillPrice = d("100")
	newer.UpdatedAt = newerAt
	c.UpsertOrder(newer)

	older := order("partial", btc, "2", enums.StatusNew)
	older.Request.Price = d("99")
	older.UpdatedAt = olderAt
	rep, err := New(nil, &snapshotExec{reports: []model.Order{older}}, c).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersUpdated != 1 {
		t.Fatalf("report=%+v, want one existing order update", rep)
	}
	got, ok := c.Order("partial")
	if !ok || got.Status != enums.StatusPartiallyFilled || !got.UpdatedAt.Equal(newerAt) {
		t.Fatalf("older snapshot regressed lifecycle: %+v ok=%v", got, ok)
	}
	if !got.Request.Price.Equal(d("101")) || !got.FilledQty.Equal(d("1")) {
		t.Fatalf("older snapshot regressed order fields: %+v", got)
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

// Position snapshots are evidence, not permission to mutate only one of the
// runtime's cache/portfolio/callback projections.
func TestReconcileAccountPositionMismatchDoesNotOverwriteOrClear(t *testing.T) {
	c := cache.New()
	// Stale local state.
	c.UpsertPosition(model.Position{InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	c.UpsertPosition(model.Position{InstrumentID: eth, Side: enums.PosNet, Quantity: d("3")})

	acct := &snapshotAccount{
		balances:        []model.AccountBalance{{Currency: "USDT", Total: d("1000"), Available: d("900")}},
		positionReports: true,
		positions: []model.Position{
			{InstrumentID: btc, Side: enums.PosNet, Quantity: d("2.5"), EntryPrice: d("60000")},
		},
	}

	r := New(acct, nil, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.BalancesUpdated != 1 || rep.PositionsUpdated != 0 || rep.PositionsCleared != 0 || rep.PositionOverwrites != 0 {
		t.Fatalf("report=%+v, want balance applied without direct position mutation", rep)
	}
	if p, ok := c.Position(btc, enums.PosNet); !ok || !p.Quantity.Equal(d("1")) {
		t.Fatalf("BTC position=%+v ok=%v, want original quantity 1", p, ok)
	}
	if p, ok := c.Position(eth, enums.PosNet); !ok || !p.Quantity.Equal(d("3")) {
		t.Fatalf("ETH position=%+v ok=%v, want original quantity 3", p, ok)
	}
	if !hasFindingCode(rep.Findings, "POSITION_MISMATCH") || rep.ActivationVerdict().Safe {
		t.Fatalf("report=%+v, want blocking position mismatch", rep)
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
		balances:        []model.AccountBalance{{Currency: "USDT", Total: d("1000"), Free: d("900")}},
		positionReports: true,
		positions: []model.Position{
			{InstrumentID: btc, Side: enums.PosNet, Quantity: d("2.5"), EntryPrice: d("60000")},
		},
	}

	r := New(acct, nil, c).WithAccountID("acct-a")
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.BalancesUpdated != 1 || rep.PositionsUpdated != 0 || rep.PositionsCleared != 0 || rep.PositionOverwrites != 0 {
		t.Fatalf("report=%+v, want balance applied without direct position mutation", rep)
	}
	if p, ok := c.PositionForAccount("acct-a", btc, enums.PosNet); !ok || !p.Quantity.Equal(d("1")) {
		t.Fatalf("acct-a BTC position=%+v ok=%v, want original qty 1", p, ok)
	}
	if p, ok := c.PositionForAccount("acct-a", eth, enums.PosNet); !ok || !p.Quantity.Equal(d("3")) {
		t.Fatalf("acct-a ETH position=%+v ok=%v, want original qty 3", p, ok)
	}
	if p, ok := c.PositionForAccount("acct-b", eth, enums.PosNet); !ok || !p.Quantity.Equal(d("7")) {
		t.Fatalf("acct-b ETH position=%+v ok=%v, want untouched qty 7", p, ok)
	}
	if b, ok := c.BalanceForAccount("acct-a", "USDT"); !ok || !b.Free.Equal(d("900")) {
		t.Fatalf("acct-a balance=%+v ok=%v, want free 900", b, ok)
	}
	if !hasFindingCode(rep.Findings, "POSITION_MISMATCH") || rep.ActivationVerdict().Safe {
		t.Fatalf("report=%+v, want scoped blocking position mismatch", rep)
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
			Reported: true,
			EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts),
			TsEvent:  ts,
			TsInit:   ts,
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

func TestReconcileOlderAccountSnapshotDoesNotClearNewerStreamPosition(t *testing.T) {
	c := cache.New()
	newerAt := time.Unix(30, 0)
	olderAt := newerAt.Add(-time.Second)
	newer := model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountMargin,
		Balances: []model.AccountBalance{{
			AccountID: model.AccountIDBinanceDefault,
			Currency:  "USDT",
			Total:     d("1000"),
			Free:      d("900"),
			UpdatedAt: newerAt,
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, newerAt),
		TsEvent:  newerAt,
		TsInit:   newerAt,
	}
	if err := c.ApplyAccountStateAt(newer, newerAt); err != nil {
		t.Fatal(err)
	}
	c.UpsertPosition(model.Position{
		AccountID:    model.AccountIDBinanceDefault,
		InstrumentID: btc,
		Side:         enums.PosNet,
		Quantity:     d("2"),
		UpdatedAt:    newerAt,
	})

	older := newer
	older.EventID = model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, olderAt)
	older.TsEvent = olderAt
	older.TsInit = olderAt
	older.Balances[0].UpdatedAt = olderAt
	acct := &snapshotAccount{hasAccountState: true, accountState: older}
	if _, err := New(acct, nil, c).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, ok := c.PositionForAccount(model.AccountIDBinanceDefault, btc, enums.PosNet); !ok || !got.Quantity.Equal(d("2")) {
		t.Fatalf("older REST snapshot cleared newer stream position: %+v ok=%v", got, ok)
	}
}

func TestReconcileNormalizesScopedAccountStateEventID(t *testing.T) {
	c := cache.New()
	ts := time.Unix(21, 0)
	acct := &snapshotAccount{
		hasAccountState: true,
		accountState: model.AccountState{
			Venue: "BINANCE",
			Type:  model.AccountMargin,
			Balances: []model.AccountBalance{{
				Currency: "USDT",
				Total:    d("1000"),
				Free:     d("950"),
			}},
			Reported: true,
			TsEvent:  ts,
			TsInit:   ts,
		},
	}

	r := New(acct, nil, c).WithAccountID(model.AccountIDBinanceDefault)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	cached, ok := c.Account(model.AccountIDBinanceDefault)
	if !ok {
		t.Fatal("account state not applied to cache")
	}
	want := model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts)
	if got := cached.LastEvent().EventID; got != want {
		t.Fatalf("event id=%q, want %q", got, want)
	}
}

func TestReconcileRefreshesAccountStateAfterLongOrderRecovery(t *testing.T) {
	c := cache.New()
	c.SetAccountStaleAfter(20 * time.Millisecond)
	firstAt := time.Unix(100, 0)
	secondAt := firstAt.Add(time.Second)
	state := func(at time.Time, free string) model.AccountState {
		return model.AccountState{
			AccountID: model.AccountIDBitgetDefault,
			Venue:     "BITGET",
			Type:      model.AccountMargin,
			Balances: []model.AccountBalance{{
				AccountID: model.AccountIDBitgetDefault,
				Currency:  "USDT",
				Total:     d("1000"),
				Free:      d(free),
				UpdatedAt: at,
			}},
			Reported: true,
			EventID:  model.AccountStateEventID("BITGET", model.AccountIDBitgetDefault, at),
			TsEvent:  at,
			TsInit:   at,
		}
	}
	account := &snapshotAccount{
		hasAccountState: true,
		accountStates: []model.AccountState{
			state(firstAt, "900"),
			state(secondAt, "800"),
		},
	}
	mass := model.NewExecutionMassStatus("BITGET", model.AccountIDBitgetDefault, secondAt)
	execution := &snapshotExec{mass: mass, massDelay: 35 * time.Millisecond}

	report, err := New(account, execution, c).WithAccountID(model.AccountIDBitgetDefault).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if account.accountStateCalls != 2 {
		t.Fatalf("account state calls=%d, want initial snapshot plus post-recovery refresh", account.accountStateCalls)
	}
	if report.AccountStatesApplied != 1 || report.BalancesUpdated != 1 {
		t.Fatalf("report=%+v, post-recovery freshness maintenance must preserve one logical account reconciliation", report)
	}
	cached, ok := c.Account(model.AccountIDBitgetDefault)
	if !ok {
		t.Fatal("refreshed account missing")
	}
	if !cached.IsFresh(time.Now()) {
		t.Fatalf("refreshed account is stale: %+v", cached.Freshness())
	}
	if got := cached.LastEvent().TsEvent; !got.Equal(secondAt) {
		t.Fatalf("cached account event at=%s, want refreshed event %s", got, secondAt)
	}
	if balance, ok := c.BalanceForAccount(model.AccountIDBitgetDefault, "USDT"); !ok || !balance.Free.Equal(d("800")) {
		t.Fatalf("refreshed balance=(%+v,%v), want free=800", balance, ok)
	}
}

func TestReconcileDoesNotMarkAccountReconciledWhenPositionsFail(t *testing.T) {
	c := cache.New()
	ts := time.Unix(20, 0)
	positionErr := errors.New("positions unavailable")
	acct := &snapshotAccount{
		hasAccountState: true,
		positionReports: true,
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
			Reported: true,
			EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts),
			TsEvent:  ts,
			TsInit:   ts,
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
		positionReports: true,
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
	if rep.BalancesUpdated != 2 || rep.PositionsUpdated != 0 || rep.PositionsCleared != 0 {
		t.Fatalf("report=%+v, want balances=2 and no direct position mutation", rep)
	}
	if b, ok := c.Balance("BTC"); !ok || !b.Total.Equal(d("2")) || !b.Available.Equal(d("2")) {
		t.Fatalf("BTC balance not reconciled: ok=%v balance=%+v", ok, b)
	}
	if b, ok := c.Balance("USDT"); !ok || !b.Total.Equal(d("800")) || !b.Available.Equal(d("800")) {
		t.Fatalf("USDT balance not reconciled: ok=%v balance=%+v", ok, b)
	}
	if p, ok := c.Position(spotBTC, enums.PosNet); !ok || !p.Quantity.Equal(d("1")) {
		t.Fatalf("legacy spot position=%+v ok=%v, mismatch path must not clear it directly", p, ok)
	}
	if !hasFindingCode(rep.Findings, "POSITION_MISMATCH") || rep.ActivationVerdict().Safe {
		t.Fatalf("report=%+v, legacy spot-position mismatch must fail closed", rep)
	}
}

func TestDerivativeFillOrderValidationCapacityRetainsEveryInput(t *testing.T) {
	existing := []model.Order{{}, {}, {}}
	reports := []*model.OrderStatusReport{{}, nil, {}}
	if got := derivativeFillOrderValidationTerminalLimit(existing, reports); got != 5 {
		t.Fatalf("validation terminal limit=%d, want one slot for every existing order and non-nil report", got)
	}
	if got := derivativeFillOrderValidationTerminalLimit(nil, nil); got != 1 {
		t.Fatalf("empty validation terminal limit=%d, want minimum safe capacity 1", got)
	}
}
