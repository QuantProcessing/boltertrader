package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/QuantProcessing/boltertrader/runtime/reconcile"
	"github.com/shopspring/decimal"
)

type failCursorJournal struct {
	*journal.MemoryJournal
	err error
}

func (j *failCursorJournal) CommitReconciliationCursor(context.Context, journal.ReconciliationCursor) error {
	return j.err
}

type recoveredFillHistoryExec struct {
	*recoveredFillExec
	exactMu      sync.Mutex
	exactOrders  []model.Order
	exactQueries []model.SingleOrderStatusQuery
}

type concurrentRecoveredFillExec struct {
	*recoveredFillExec

	mu             sync.Mutex
	exactOrders    map[string]model.Order
	exactErrors    map[string]error
	exactDelays    map[string]time.Duration
	exactQueries   []model.SingleOrderStatusQuery
	exactActive    int
	exactMaxActive int
}

func newConcurrentRecoveredFillExec(mass *model.ExecutionMassStatus, orders ...model.Order) *concurrentRecoveredFillExec {
	exactOrders := make(map[string]model.Order, len(orders))
	for _, order := range orders {
		exactOrders[order.VenueOrderID] = order
	}
	return &concurrentRecoveredFillExec{
		recoveredFillExec: &recoveredFillExec{mass: mass, events: make(chan contract.ExecEnvelope, 4)},
		exactOrders:       exactOrders,
		exactErrors:       make(map[string]error),
		exactDelays:       make(map[string]time.Duration),
	}
}

func (e *concurrentRecoveredFillExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{Venue: "RECOVERY", Reports: contract.ReportCapabilities{SingleOrderStatus: true, FillHistory: true}}
}

func (e *concurrentRecoveredFillExec) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.mu.Lock()
	e.exactQueries = append(e.exactQueries, query)
	e.exactActive++
	if e.exactActive > e.exactMaxActive {
		e.exactMaxActive = e.exactActive
	}
	delay := e.exactDelays[query.VenueOrderID]
	order, orderOK := e.exactOrders[query.VenueOrderID]
	err := e.exactErrors[query.VenueOrderID]
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		e.exactActive--
		e.mu.Unlock()
	}()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return nil, err
	}
	if !orderOK || !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{
		InstrumentID: query.InstrumentID,
		AccountID:    query.AccountID,
		ClientID:     query.ClientID,
		VenueOrderID: query.VenueOrderID,
	}) {
		return nil, nil
	}
	return &model.OrderStatusReport{Venue: order.Request.InstrumentID.Venue, AccountID: order.Request.AccountID, Order: order}, nil
}

func (e *concurrentRecoveredFillExec) exactStats() (queries []model.SingleOrderStatusQuery, maxActive int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.SingleOrderStatusQuery(nil), e.exactQueries...), e.exactMaxActive
}

func newRecoveredFillHistoryExec(mass *model.ExecutionMassStatus, exactOrders ...model.Order) *recoveredFillHistoryExec {
	return &recoveredFillHistoryExec{
		recoveredFillExec: &recoveredFillExec{mass: mass, events: make(chan contract.ExecEnvelope, 4)},
		exactOrders:       exactOrders,
	}
}

func (e *recoveredFillHistoryExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{Venue: "RECOVERY", Reports: contract.ReportCapabilities{SingleOrderStatus: true, FillHistory: true}}
}

func (e *recoveredFillHistoryExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.exactMu.Lock()
	e.exactQueries = append(e.exactQueries, query)
	e.exactMu.Unlock()
	for _, order := range e.exactOrders {
		if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{
			InstrumentID: query.InstrumentID,
			AccountID:    query.AccountID,
			ClientID:     query.ClientID,
			VenueOrderID: query.VenueOrderID,
		}) {
			continue
		}
		return &model.OrderStatusReport{Venue: order.Request.InstrumentID.Venue, AccountID: order.Request.AccountID, Order: order}, nil
	}
	return nil, nil
}

func recoveryOrder(id model.InstrumentID, clientID, venueID, qty string, at time.Time) model.Order {
	return model.Order{
		Request: model.OrderRequest{
			AccountID: "acct", InstrumentID: id, ClientID: clientID,
			Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC,
			Quantity: decimal.RequireFromString(qty), Price: decimal.NewFromInt(100), PositionSide: enums.PosNet,
		},
		VenueOrderID: venueID, Status: enums.StatusNew, CreatedAt: at, UpdatedAt: at,
	}
}

func journalRecordCount(store *journal.MemoryJournal, recordType journal.RecordType) int {
	count := 0
	for _, record := range store.Records() {
		if record.Type == recordType {
			count++
		}
	}
	return count
}

func assertNoRecoveredFillMutation(
	t *testing.T,
	report reconcile.Report,
	node *TradingNode,
	store *journal.MemoryJournal,
	id model.InstrumentID,
	accountIDs []string,
	callbacks int,
) {
	t.Helper()
	if report.OrdersMaterialized != 0 || report.FillsApplied != 0 || report.CursorsCommitted != 0 {
		t.Fatalf("report=%+v, want zero materialized orders, applied fills, and cursor commits", report)
	}
	if orders := node.Cache.Orders(); len(orders) != 0 {
		t.Fatalf("cache orders=%+v, want none", orders)
	}
	for _, accountID := range accountIDs {
		for _, side := range []enums.PositionSide{enums.PosNet, enums.PosLong, enums.PosShort} {
			if qty := node.Portfolio.NetQtyForAccount(accountID, id, side); !qty.IsZero() {
				t.Fatalf("portfolio qty for %s/%s=%s, want zero", accountID, side, qty)
			}
		}
	}
	if callbacks != 0 {
		t.Fatalf("fill callbacks=%d, want zero", callbacks)
	}
	if got := journalRecordCount(store, journal.RecordAppliedEvent); got != 0 {
		t.Fatalf("applied-event records=%d, want zero", got)
	}
	if got := journalRecordCount(store, journal.RecordReconciliationCursor); got != 0 {
		t.Fatalf("cursor records=%d, want zero", got)
	}
}

func TestFreshNodeMaterializesAuthoritativeRecoveredSpotFillWithClientID(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 13, 10, 55, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "authoritative-client",
		VenueOrderID: "authoritative-venue", TradeID: "authoritative-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Second))
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	callbacks := 0
	node := NewNode(
		Clients{Execution: newRecoveredFillHistoryExec(mass)},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"authoritative-recovery",
		WithOnFill(func(got model.Fill) {
			callbacks++
			if got.TradeID != fill.TradeID {
				t.Fatalf("callback fill=%+v, want trade %s", got, fill.TradeID)
			}
		}),
	)

	report, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("first resync: %v", err)
	}
	if report.OrdersMaterialized != 1 || report.FillsApplied != 1 {
		t.Fatalf("first report=%+v, want one materialized order and one applied fill", report)
	}
	order, ok := node.Cache.OrderForAccount("acct", fill.ClientID)
	if !ok {
		t.Fatal("materialized recovered order missing")
	}
	if order.VenueOrderID != fill.VenueOrderID || order.Status != enums.StatusFilled || !order.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("materialized order=%+v, want filled qty 1", order)
	}
	venueOrder, ok := node.Cache.OrderByVenueOrderIDForAccount("acct", fill.VenueOrderID)
	if !ok {
		t.Fatal("materialized recovered order missing from venue-id cache lookup")
	}
	if venueOrder.Request.ClientID != fill.ClientID || venueOrder.Status != enums.StatusFilled || !venueOrder.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("venue-id order=%+v, want authoritative client id and filled qty 1", venueOrder)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty=%s, want 1", qty)
	}
	if callbacks != 1 {
		t.Fatalf("fill callbacks=%d, want 1", callbacks)
	}

	second, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("second resync: %v", err)
	}
	if second.OrdersMaterialized != 0 || second.FillsApplied != 0 || second.FillsDuplicate != 1 {
		t.Fatalf("second report=%+v, want one duplicate and no repeated materialization/application", second)
	}
	if callbacks != 1 {
		t.Fatalf("fill callbacks after duplicate recovery=%d, want 1", callbacks)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty after duplicate recovery=%s, want 1", qty)
	}
}

func TestFreshNodeHydratesAuthoritativeRecoveredPerpPositionSide(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 55, 30, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	tests := []struct {
		name         string
		orderSide    enums.OrderSide
		positionSide enums.PositionSide
		wantQty      decimal.Decimal
	}{
		{name: "long leg", orderSide: enums.SideBuy, positionSide: enums.PosLong, wantQty: decimal.NewFromInt(1)},
		{name: "short leg", orderSide: enums.SideSell, positionSide: enums.PosShort, wantQty: decimal.NewFromInt(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fill := model.Fill{
				AccountID: "acct", InstrumentID: id, ClientID: "hedge-client-" + tt.name,
				VenueOrderID: "hedge-venue-" + tt.name, TradeID: "hedge-trade-" + tt.name, Side: tt.orderSide,
				Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
			}
			mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Second))
			if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
				t.Fatalf("add fill report: %v", err)
			}
			order := recoveryOrder(id, fill.ClientID, fill.VenueOrderID, "1", at)
			order.Request.Side = tt.orderSide
			order.Request.PositionSide = tt.positionSide
			order.Status = enums.StatusFilled
			order.FilledQty = decimal.NewFromInt(1)
			order.AvgFillPrice = fill.Price
			exec := newRecoveredFillHistoryExec(mass, order)
			callbacks := 0
			node := NewNode(
				Clients{Execution: exec},
				clock.NewSimulatedClock(mass.GeneratedAt),
				"authoritative-hedge-recovery",
				WithOnFill(func(model.Fill) { callbacks++ }),
			)

			report, err := node.Resync(context.Background())
			if err != nil {
				t.Fatalf("resync: %v", err)
			}
			if report.OrdersMaterialized != 0 || report.FillsApplied != 1 {
				t.Fatalf("report=%+v, want exact-order hydration and one applied fill", report)
			}
			if len(exec.exactQueries) != 1 {
				t.Fatalf("exact-order queries=%+v, want one", exec.exactQueries)
			}
			query := exec.exactQueries[0]
			if query.AccountID != fill.AccountID || query.InstrumentID != fill.InstrumentID || query.ClientID != fill.ClientID || query.VenueOrderID != fill.VenueOrderID {
				t.Fatalf("exact-order query=%+v, want fill identity", query)
			}
			got, ok := node.Cache.OrderForAccount("acct", fill.ClientID)
			if !ok || got.Request.PositionSide != tt.positionSide || got.Status != enums.StatusFilled {
				t.Fatalf("hydrated order=(%+v,%v), want position side %s and FILLED", got, ok, tt.positionSide)
			}
			if qty := node.Portfolio.NetQtyForAccount("acct", id, tt.positionSide); !qty.Equal(tt.wantQty) {
				t.Fatalf("portfolio %s qty=%s, want %s", tt.positionSide, qty, tt.wantQty)
			}
			if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.IsZero() {
				t.Fatalf("portfolio net qty=%s, want zero for hedge recovery", qty)
			}
			if callbacks != 1 {
				t.Fatalf("fill callbacks=%d, want 1", callbacks)
			}
		})
	}
}

func TestRecoveredDerivativeOrdersArePrefetchedWithBoundedConcurrency(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	orders := make([]model.Order, 0, 8)
	wantTrades := make([]string, 0, 9)
	for i := 0; i < 8; i++ {
		clientID := fmt.Sprintf("prefetch-client-%02d", i)
		venueID := fmt.Sprintf("prefetch-venue-%02d", i)
		orderQty := "1"
		if i == 0 {
			orderQty = "2"
		}
		order := recoveryOrder(id, clientID, venueID, orderQty, at)
		order.Request.PositionSide = enums.PosLong
		order.Status = enums.StatusFilled
		order.FilledQty = decimal.RequireFromString(orderQty)
		order.AvgFillPrice = decimal.NewFromInt(100)
		orders = append(orders, order)

		tradeID := fmt.Sprintf("prefetch-trade-%02d", i)
		fill := model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueID,
			TradeID: tradeID, Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
			Timestamp: at.Add(time.Duration(i) * time.Second),
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
		wantTrades = append(wantTrades, tradeID)
	}
	extraFill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: orders[0].Request.ClientID, VenueOrderID: orders[0].VenueOrderID,
		TradeID: "prefetch-trade-08", Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
		Timestamp: at.Add(8 * time.Second),
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: extraFill, ReportedAt: extraFill.Timestamp}); err != nil {
		t.Fatalf("add duplicate-order fill report: %v", err)
	}
	wantTrades = append(wantTrades, extraFill.TradeID)

	exec := newConcurrentRecoveredFillExec(mass, orders...)
	for i, order := range orders {
		exec.exactDelays[order.VenueOrderID] = time.Duration(8-i) * 10 * time.Millisecond
	}
	var callbacksMu sync.Mutex
	var callbackTrades []string
	node := NewNode(
		Clients{Execution: exec},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"bounded-order-prefetch",
		WithOnFill(func(fill model.Fill) {
			callbacksMu.Lock()
			callbackTrades = append(callbackTrades, fill.TradeID)
			callbacksMu.Unlock()
		}),
	)
	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	queries, maxActive := exec.exactStats()
	if len(queries) != len(orders) {
		t.Fatalf("exact-order queries=%d, want one per %d unique orders", len(queries), len(orders))
	}
	if maxActive <= 1 || maxActive > 4 {
		t.Fatalf("max concurrent exact-order queries=%d, want 2..4", maxActive)
	}
	if report.OrdersExternal != len(orders) || report.FillsApplied != len(wantTrades) {
		t.Fatalf("report=%+v, want %d hydrated orders and %d fills", report, len(orders), len(wantTrades))
	}
	callbacksMu.Lock()
	gotTrades := append([]string(nil), callbackTrades...)
	callbacksMu.Unlock()
	if fmt.Sprint(gotTrades) != fmt.Sprint(wantTrades) {
		t.Fatalf("callback trades=%v, want timestamp order %v", gotTrades, wantTrades)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosLong); !qty.Equal(decimal.NewFromInt(int64(len(wantTrades)))) {
		t.Fatalf("portfolio long qty=%s, want %d", qty, len(wantTrades))
	}
}

func TestRecoveredDerivativeOrderPrefetchFailsBeforeFillMutation(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 5, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	orders := make([]model.Order, 0, 2)
	for i := 0; i < 2; i++ {
		clientID := fmt.Sprintf("atomic-client-%d", i)
		venueID := fmt.Sprintf("atomic-venue-%d", i)
		order := recoveryOrder(id, clientID, venueID, "1", at)
		order.Request.PositionSide = enums.PosLong
		order.Status = enums.StatusFilled
		order.FilledQty = decimal.NewFromInt(1)
		order.AvgFillPrice = decimal.NewFromInt(100)
		orders = append(orders, order)
		fill := model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueID,
			TradeID: fmt.Sprintf("atomic-trade-%d", i), Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
			Timestamp: at.Add(time.Duration(i) * time.Second),
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
	}
	exec := newConcurrentRecoveredFillExec(mass, orders...)
	injected := errors.New("injected exact-order lookup failure")
	exec.exactErrors[orders[1].VenueOrderID] = injected
	exec.exactDelays[orders[0].VenueOrderID] = 5 * time.Millisecond
	exec.exactDelays[orders[1].VenueOrderID] = 10 * time.Millisecond
	store := journal.NewMemory()
	callbacks := 0
	node := NewNode(
		Clients{Execution: exec},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"atomic-order-prefetch",
		WithJournal(store),
		WithOnFill(func(model.Fill) { callbacks++ }),
	)
	report, err := node.Resync(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("resync error=%v, want %v", err, injected)
	}
	if report.OrdersExternal != 0 {
		t.Fatalf("report=%+v, want no hydrated order before prefetch succeeds", report)
	}
	queries, maxActive := exec.exactStats()
	if len(queries) != 2 || maxActive != 2 {
		t.Fatalf("exact-order queries=%d max-active=%d, want both lookups in flight before the failure is returned", len(queries), maxActive)
	}
	assertNoRecoveredFillMutation(t, report, node, store, id, []string{"acct"}, callbacks)
}

func TestRecoveredDerivativeOrderPrefetchStopsSchedulingAfterFirstError(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 10, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	orders := make([]model.Order, 0, 12)
	for i := 0; i < 12; i++ {
		clientID := fmt.Sprintf("cancel-client-%02d", i)
		venueID := fmt.Sprintf("cancel-venue-%02d", i)
		order := recoveryOrder(id, clientID, venueID, "1", at)
		order.Request.PositionSide = enums.PosLong
		order.Status = enums.StatusFilled
		order.FilledQty = decimal.NewFromInt(1)
		order.AvgFillPrice = decimal.NewFromInt(100)
		orders = append(orders, order)
		fill := model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueID,
			TradeID: fmt.Sprintf("cancel-trade-%02d", i), Side: enums.SideBuy,
			Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(time.Duration(i) * time.Second),
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
	}
	exec := newConcurrentRecoveredFillExec(mass, orders...)
	injected := errors.New("stop exact-order prefetch")
	exec.exactErrors[orders[0].VenueOrderID] = injected
	for _, order := range orders[1:] {
		exec.exactDelays[order.VenueOrderID] = 100 * time.Millisecond
	}
	store := journal.NewMemory()
	callbacks := 0
	node := NewNode(
		Clients{Execution: exec},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"cancel-order-prefetch",
		WithJournal(store),
		WithOnFill(func(model.Fill) { callbacks++ }),
	)
	report, err := node.Resync(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("resync error=%v, want %v", err, injected)
	}
	queries, maxActive := exec.exactStats()
	const wantMaxPrefetch = 4
	if len(queries) == 0 || len(queries) > wantMaxPrefetch {
		t.Fatalf("exact-order queries=%d, want only the initial bounded worker set", len(queries))
	}
	if maxActive > wantMaxPrefetch {
		t.Fatalf("max concurrent exact-order queries=%d, want <=%d", maxActive, wantMaxPrefetch)
	}
	assertNoRecoveredFillMutation(t, report, node, store, id, []string{"acct"}, callbacks)
}

func TestRecoveredDerivativeOrderPrefetchRejectsCrossReportAliasConflictBeforeFillMutation(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 15, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	const sharedClientID = "conflicting-client"
	orders := make([]model.Order, 0, 2)
	for i := 0; i < 2; i++ {
		venueID := fmt.Sprintf("conflicting-venue-%d", i)
		order := recoveryOrder(id, sharedClientID, venueID, "1", at)
		order.Request.PositionSide = enums.PosLong
		order.Status = enums.StatusFilled
		order.FilledQty = decimal.NewFromInt(1)
		order.AvgFillPrice = decimal.NewFromInt(100)
		orders = append(orders, order)
		fill := model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: sharedClientID, VenueOrderID: venueID,
			TradeID: fmt.Sprintf("conflicting-trade-%d", i), Side: enums.SideBuy,
			Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(time.Duration(i) * time.Second),
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
	}
	exec := newConcurrentRecoveredFillExec(mass, orders...)
	store := journal.NewMemory()
	callbacks := 0
	node := NewNode(
		Clients{Execution: exec},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"conflicting-order-prefetch",
		WithJournal(store),
		WithOnFill(func(model.Fill) { callbacks++ }),
	)
	report, err := node.Resync(context.Background())
	if !errors.Is(err, cache.ErrOrderIdentityConflict) {
		t.Fatalf("resync error=%v, want typed order identity conflict", err)
	}
	assertNoRecoveredFillMutation(t, report, node, store, id, []string{"acct"}, callbacks)
}

func TestRecoveredDerivativeOrderPrefetchPropagatesAlreadyCanceledContext(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 20, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "canceled-client", "canceled-venue", "1", at)
	order.Request.PositionSide = enums.PosLong
	order.Status = enums.StatusFilled
	order.FilledQty = decimal.NewFromInt(1)
	order.AvgFillPrice = decimal.NewFromInt(100)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID, VenueOrderID: order.VenueOrderID,
		TradeID: "canceled-trade", Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	exec := newConcurrentRecoveredFillExec(mass, order)
	store := journal.NewMemory()
	callbacks := 0
	node := NewNode(
		Clients{Execution: exec},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"canceled-order-prefetch",
		WithJournal(store),
		WithOnFill(func(model.Fill) { callbacks++ }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := node.Resync(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resync error=%v, want context canceled", err)
	}
	if queries, _ := exec.exactStats(); len(queries) != 0 {
		t.Fatalf("exact-order queries=%d, want none after pre-cancel", len(queries))
	}
	assertNoRecoveredFillMutation(t, report, node, store, id, []string{"acct"}, callbacks)
}

func TestOrdinaryUnknownClientFillRemainsUnmatched(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 55, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "authoritative-client",
		VenueOrderID: "authoritative-venue", TradeID: "authoritative-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	tests := []struct {
		name string
		env  contract.ExecEnvelope
	}{
		{name: "ordinary metadata", env: contract.NewExecEnvelope(contract.FillEvent{Fill: fill})},
		{name: "forged reconciliation metadata", env: contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, contract.EventMeta{
			Source: contract.SourceReconciliation,
			Venue:  "RECOVERY",
			Flags:  contract.EventFlagFromSnapshot | contract.EventFlagFromReconciliation,
		})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callbacks := 0
			node := NewNode(Clients{}, clock.NewSimulatedClock(at), "acct", WithOnFill(func(model.Fill) { callbacks++ }))

			if got := node.applyFillResult(fill, tt.env); got != reconcile.FillApplyUnmatched {
				t.Fatalf("apply result=%d, want unmatched", got)
			}
			if orders := node.Cache.Orders(); len(orders) != 0 {
				t.Fatalf("cache orders=%+v after unmatched fill, want none", orders)
			}
			if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.IsZero() {
				t.Fatalf("portfolio qty=%s after unmatched fill, want zero", qty)
			}
			if callbacks != 0 {
				t.Fatalf("fill callbacks=%d after unmatched fill, want zero", callbacks)
			}
		})
	}
}

func TestOrdinaryExternalSpotFillRejectsInvalidEconomics(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 55, 45, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	tests := []struct {
		name     string
		side     enums.OrderSide
		price    decimal.Decimal
		quantity decimal.Decimal
	}{
		{name: "unknown_side", side: enums.SideUnknown, price: decimal.NewFromInt(100), quantity: decimal.NewFromInt(1)},
		{name: "invalid_side", side: enums.OrderSide(255), price: decimal.NewFromInt(100), quantity: decimal.NewFromInt(1)},
		{name: "zero_price", side: enums.SideBuy, price: decimal.Zero, quantity: decimal.NewFromInt(1)},
		{name: "negative_price", side: enums.SideBuy, price: decimal.NewFromInt(-1), quantity: decimal.NewFromInt(1)},
		{name: "zero_quantity", side: enums.SideBuy, price: decimal.NewFromInt(100), quantity: decimal.Zero},
		{name: "negative_quantity", side: enums.SideBuy, price: decimal.NewFromInt(100), quantity: decimal.NewFromInt(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fill := model.Fill{
				AccountID: "acct", InstrumentID: id, VenueOrderID: "external-" + tt.name,
				TradeID: "trade-" + tt.name, Side: tt.side, Price: tt.price, Quantity: tt.quantity, Timestamp: at,
			}
			callbacks := 0
			node := NewNode(Clients{}, clock.NewSimulatedClock(at), "acct", WithOnFill(func(model.Fill) { callbacks++ }))

			if got := node.applyFillResult(fill, contract.NewExecEnvelope(contract.FillEvent{Fill: fill})); got != reconcile.FillApplyUnmatched {
				t.Fatalf("apply result=%d, want unmatched", got)
			}
			if orders := node.Cache.Orders(); len(orders) != 0 {
				t.Fatalf("cache orders=%+v after invalid fill, want none", orders)
			}
			if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.IsZero() {
				t.Fatalf("portfolio qty=%s after invalid fill, want zero", qty)
			}
			if callbacks != 0 {
				t.Fatalf("fill callbacks=%d after invalid fill, want zero", callbacks)
			}
		})
	}
}

func TestKnownOrderFillRejectsInvalidEconomics(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 55, 50, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	tests := []struct {
		name     string
		side     enums.OrderSide
		price    decimal.Decimal
		quantity decimal.Decimal
	}{
		{name: "invalid_side", side: enums.OrderSide(255), price: decimal.NewFromInt(100), quantity: decimal.NewFromInt(1)},
		{name: "zero_price", side: enums.SideBuy, price: decimal.Zero, quantity: decimal.NewFromInt(1)},
		{name: "negative_price", side: enums.SideBuy, price: decimal.NewFromInt(-1), quantity: decimal.NewFromInt(1)},
		{name: "zero_quantity", side: enums.SideBuy, price: decimal.NewFromInt(100), quantity: decimal.Zero},
		{name: "negative_quantity", side: enums.SideBuy, price: decimal.NewFromInt(100), quantity: decimal.NewFromInt(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := recoveryOrder(id, "known-client-"+tt.name, "known-venue-"+tt.name, "1", at)
			fill := model.Fill{
				AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
				VenueOrderID: order.VenueOrderID, TradeID: "known-trade-" + tt.name,
				Side: tt.side, Price: tt.price, Quantity: tt.quantity, Timestamp: at,
			}
			callbacks := 0
			node := NewNode(Clients{}, clock.NewSimulatedClock(at), "acct", WithOnFill(func(model.Fill) { callbacks++ }))
			node.Cache.UpsertOrder(order)

			if got := node.applyFillResult(fill, contract.NewExecEnvelope(contract.FillEvent{Fill: fill})); got != reconcile.FillApplyUnmatched {
				t.Fatalf("apply result=%d, want unmatched", got)
			}
			got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
			if !ok || got.Status != enums.StatusNew || !got.FilledQty.IsZero() {
				t.Fatalf("cached order=(%+v,%v) after invalid fill, want unchanged NEW order", got, ok)
			}
			if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.IsZero() {
				t.Fatalf("portfolio qty=%s after invalid fill, want zero", qty)
			}
			if callbacks != 0 {
				t.Fatalf("fill callbacks=%d after invalid fill, want zero", callbacks)
			}
		})
	}
}

func TestInvalidLiveFillIsRejectedWithoutBuffering(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 55, 55, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	tests := []struct {
		name     string
		side     enums.OrderSide
		price    decimal.Decimal
		quantity decimal.Decimal
	}{
		{name: "invalid_side", side: enums.OrderSide(255), price: decimal.NewFromInt(100), quantity: decimal.NewFromInt(1)},
		{name: "zero_price", side: enums.SideBuy, price: decimal.Zero, quantity: decimal.NewFromInt(1)},
		{name: "zero_quantity", side: enums.SideBuy, price: decimal.NewFromInt(100), quantity: decimal.Zero},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fill := model.Fill{
				AccountID: "acct", InstrumentID: id, VenueOrderID: "invalid-live-" + tt.name,
				TradeID: "invalid-live-trade-" + tt.name, Side: tt.side,
				Price: tt.price, Quantity: tt.quantity, Timestamp: at,
			}
			callbacks := 0
			node := NewNode(Clients{}, clock.NewSimulatedClock(at), "acct", WithOnFill(func(model.Fill) { callbacks++ }))

			node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: fill}))

			if got := node.Metrics().PendingFills; got != 0 {
				t.Fatalf("pending fills=%d after invalid live fill, want zero", got)
			}
			if state := node.State(); state.Trading != lifecycle.TradingHalted {
				t.Fatalf("trading state=%s after invalid live fill, want halted", state.Trading)
			}
			if len(node.Cache.Orders()) != 0 || callbacks != 0 {
				t.Fatalf("orders=%+v callbacks=%d after invalid live fill, want no mutation", node.Cache.Orders(), callbacks)
			}
		})
	}
}

func TestUnresolvableLiveFillSideDoesNotRebuffer(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 55, 58, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "unknown-side-client", "unknown-side-venue", "1", at)
	order.Request.Side = enums.SideUnknown
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "unknown-side-trade", Side: enums.SideUnknown,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	callbacks := 0
	node := NewNode(Clients{}, clock.NewSimulatedClock(at), "acct", WithOnFill(func(model.Fill) { callbacks++ }))
	node.Cache.UpsertOrder(order)

	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: fill}))

	if got := node.Metrics().PendingFills; got != 0 {
		t.Fatalf("pending fills=%d after unresolvable known-order fill, want zero", got)
	}
	if state := node.State(); state.Trading != lifecycle.TradingHalted {
		t.Fatalf("trading state=%s after unresolvable known-order fill, want halted", state.Trading)
	}
	got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
	if !ok || got.Status != enums.StatusNew || !got.FilledQty.IsZero() || callbacks != 0 {
		t.Fatalf("order=(%+v,%v) callbacks=%d after unresolvable fill, want unchanged order and no callback", got, ok, callbacks)
	}
}

func TestAuthoritativeRecoveredFillFailsClosedWithoutMutation(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 56, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	tests := []struct {
		name          string
		side          enums.OrderSide
		price         decimal.Decimal
		exactOrder    bool
		finding       string
		wantError     bool
		fillAccount   string
		checkAccounts []string
	}{
		{name: "valid_economics_without_order", side: enums.SideBuy, price: decimal.NewFromInt(100), finding: "FILL_WITHOUT_ORDER"},
		{name: "unknown_side", side: enums.SideUnknown, price: decimal.NewFromInt(100), finding: "FILL_WITHOUT_ORDER"},
		{name: "invalid_side_with_exact_order", side: enums.OrderSide(255), price: decimal.NewFromInt(100), exactOrder: true, finding: "FILL_INVALID_ECONOMICS"},
		{name: "zero_price_with_exact_order", side: enums.SideBuy, price: decimal.Zero, exactOrder: true, finding: "FILL_INVALID_ECONOMICS"},
		{name: "negative_price_with_exact_order", side: enums.SideBuy, price: decimal.NewFromInt(-1), exactOrder: true, finding: "FILL_INVALID_ECONOMICS"},
		{name: "side_mismatch_with_exact_order", side: enums.SideSell, price: decimal.NewFromInt(100), exactOrder: true, wantError: true},
		{name: "wrong_account", side: enums.SideBuy, price: decimal.NewFromInt(100), fillAccount: "other-acct", checkAccounts: []string{"acct", "other-acct"}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fillAccount := tt.fillAccount
			if fillAccount == "" {
				fillAccount = "acct"
			}
			fill := model.Fill{
				AccountID: fillAccount, InstrumentID: id, ClientID: "invalid-client-" + tt.name,
				VenueOrderID: "invalid-venue-" + tt.name, TradeID: "invalid-trade-" + tt.name, Side: tt.side,
				Price: tt.price, Quantity: decimal.NewFromInt(1), Timestamp: at,
			}
			mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Second))
			if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
				t.Fatalf("add fill report: %v", err)
			}
			store := journal.NewMemory()
			callbacks := 0
			exactOrders := []model.Order(nil)
			if tt.exactOrder {
				exactOrders = append(exactOrders, recoveryOrder(id, fill.ClientID, fill.VenueOrderID, "1", at))
			}
			node := NewNode(
				Clients{Execution: newRecoveredFillHistoryExec(mass, exactOrders...)},
				clock.NewSimulatedClock(mass.GeneratedAt),
				"invalid-authoritative-fill",
				WithJournal(store),
				WithOnFill(func(model.Fill) { callbacks++ }),
			)

			report, err := node.Resync(context.Background())
			if tt.wantError && err == nil {
				t.Fatal("resync error=nil, want exact-order identity mismatch")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("resync: %v", err)
			}
			if tt.finding != "" {
				foundBlocking := false
				for _, finding := range report.Findings {
					if finding.Blocking && finding.Code == tt.finding {
						foundBlocking = true
						break
					}
				}
				if !foundBlocking {
					t.Fatalf("findings=%+v, want blocking %s finding", report.Findings, tt.finding)
				}
			}
			checkAccounts := tt.checkAccounts
			if len(checkAccounts) == 0 {
				checkAccounts = []string{"acct"}
			}
			assertNoRecoveredFillMutation(t, report, node, store, id, checkAccounts, callbacks)
		})
	}
}

func TestRecoveredFillWithUnknownSideUsesKnownOrderSide(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 56, 30, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "known-side-client", "known-side-venue", "1", at)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "known-side-trade", Side: enums.SideUnknown,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Second))
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	callbacks := 0
	callbackSide := enums.SideUnknown
	node := NewNode(
		Clients{Execution: newRecoveredFillHistoryExec(mass)},
		clock.NewSimulatedClock(mass.GeneratedAt),
		"known-side-recovery",
		WithOnFill(func(got model.Fill) {
			callbacks++
			callbackSide = got.Side
		}),
	)
	node.Cache.UpsertOrder(order)

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if report.OrdersMaterialized != 0 || report.FillsApplied != 1 {
		t.Fatalf("report=%+v, want known order and one applied fill", report)
	}
	got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
	if !ok || got.Status != enums.StatusFilled || !got.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("known order=(%+v,%v), want filled qty 1", got, ok)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty=%s, want 1", qty)
	}
	if callbacks != 1 || callbackSide != enums.SideBuy {
		t.Fatalf("callbacks=%d side=%s, want one normalized buy fill", callbacks, callbackSide)
	}
}

func TestRecoveredFillDoesNotDoubleCountCumulativeOrderSnapshot(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "cumulative-client", "cumulative-venue", "2", at)
	order.Status = enums.StatusPartiallyFilled
	order.FilledQty = decimal.NewFromInt(1)
	order.AvgFillPrice = decimal.NewFromInt(100)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "cumulative-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Second))
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "RECOVERY", AccountID: "acct", Order: order, ReportedAt: at.Add(time.Second)}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at.Add(time.Second)}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	node := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "cumulative")

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
	if !ok {
		t.Fatal("reconciled order missing")
	}
	if report.FillsApplied != 1 || !got.FilledQty.Equal(decimal.NewFromInt(1)) || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("report=%+v order=%+v, cumulative snapshot fill was counted twice", report, got)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty=%s, want one recovered fill", qty)
	}
}

func TestCumulativeSnapshotDoesNotRegressPreviouslyAppliedFills(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 5, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "nonregression-client", "nonregression-venue", "3", at)
	callbacks := 0
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	snapshot := order
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = decimal.NewFromInt(1)
	snapshot.AvgFillPrice = decimal.NewFromInt(100)
	snapshot.UpdatedAt = time.Time{}
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "RECOVERY", AccountID: "acct", Order: snapshot, ReportedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	second := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "nonregression-2", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(2 * time.Second),
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: second, ReportedAt: second.Timestamp}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	node := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "nonregression", WithOnFill(func(model.Fill) { callbacks++ }))
	node.Cache.UpsertOrder(order)
	for i, tradeID := range []string{"nonregression-1", "nonregression-2"} {
		fill := second
		fill.TradeID = tradeID
		fill.Timestamp = at.Add(time.Duration(i+1) * time.Second)
		node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: fill}))
	}
	if callbacks != 2 {
		t.Fatalf("live callbacks=%d, want 2", callbacks)
	}

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
	if !ok || !got.FilledQty.Equal(decimal.NewFromInt(2)) || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("report=%+v order=%+v, older cumulative snapshot regressed applied fills", report, got)
	}
	if callbacks != 2 || report.FillsDuplicate != 1 {
		t.Fatalf("callbacks=%d report=%+v, duplicate fill was re-emitted", callbacks, report)
	}
}

func TestCumulativeSnapshotReappliesOnlyUncoveredRecoveredFills(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 7, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	snapshot := recoveryOrder(id, "partial-coverage-client", "partial-coverage-venue", "3", at)
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = decimal.NewFromInt(1)
	snapshot.AvgFillPrice = decimal.NewFromInt(100)
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "RECOVERY", AccountID: "acct", Order: snapshot, ReportedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	for i, price := range []int64{100, 200} {
		fill := model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: snapshot.Request.ClientID,
			VenueOrderID: snapshot.VenueOrderID, TradeID: fmt.Sprintf("partial-coverage-%d", i+1),
			Side: enums.SideBuy, Price: decimal.NewFromInt(price), Quantity: decimal.NewFromInt(1),
			Timestamp: at.Add(time.Duration(i+1) * time.Second),
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
	}
	node := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "partial-coverage")
	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, ok := node.Cache.OrderForAccount("acct", snapshot.Request.ClientID)
	if !ok || !got.FilledQty.Equal(decimal.NewFromInt(2)) || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("report=%+v order=%+v, snapshot-covered fill was added twice", report, got)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("portfolio qty=%s, want both recovered fills", qty)
	}
}

func TestLiveFillIdentityEnrichmentRemainsDuplicate(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 10, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "identity-client", "identity-venue", "2", at)
	recovered := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "identity-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: recovered, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	callbacks := 0
	node := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "identity", WithOnFill(func(model.Fill) { callbacks++ }))
	node.Cache.UpsertOrder(order)
	live := recovered
	live.ClientID = ""
	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: live}))

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if callbacks != 1 || report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("callbacks=%d report=%+v, identity enrichment re-applied one venue trade", callbacks, report)
	}
}

func TestRestartSeedsDurableRecoveredFillDedupe(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 20, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "SOL-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "restart-client", "restart-venue", "2", at)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "restart-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	j := journal.NewMemory()
	firstCallbacks := 0
	first := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "restart",
		WithJournal(j), WithOnFill(func(model.Fill) { firstCallbacks++ }))
	first.Cache.UpsertOrder(order)
	if _, err := first.Resync(context.Background()); err != nil {
		t.Fatalf("first resync: %v", err)
	}
	if firstCallbacks != 1 {
		t.Fatalf("first callbacks=%d, want 1", firstCallbacks)
	}

	secondCallbacks := 0
	second := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "restart",
		WithJournal(j), WithOnFill(func(model.Fill) { secondCallbacks++ }))
	second.Cache.UpsertOrder(order)
	report, err := second.Resync(context.Background())
	if err != nil {
		t.Fatalf("second resync: %v", err)
	}
	if secondCallbacks != 0 || report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("second callbacks=%d report=%+v, durable terminal fill was emitted again", secondCallbacks, report)
	}
}

func TestRestartSeedsAppliedFillWhenCursorCommitFailed(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 25, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "SOL-USDT", Kind: enums.KindPerp}
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "failed-cursor-client",
		VenueOrderID: "failed-cursor-venue", TradeID: "failed-cursor-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	order := recoveryOrder(id, fill.ClientID, fill.VenueOrderID, "1", at)
	order.Request.PositionSide = enums.PosLong
	order.Status = enums.StatusFilled
	order.FilledQty = fill.Quantity
	order.AvgFillPrice = fill.Price
	underlying := journal.NewMemory()
	fail := errors.New("injected cursor commit failure")
	firstCallbacks := 0
	first := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass, order)}, clock.NewSimulatedClock(mass.GeneratedAt), "failed-cursor",
		WithJournal(&failCursorJournal{MemoryJournal: underlying, err: fail}),
		WithOnFill(func(model.Fill) { firstCallbacks++ }))
	firstReport, err := first.Resync(context.Background())
	if !errors.Is(err, fail) {
		t.Fatalf("first resync err=%v, want %v", err, fail)
	}
	if firstReport.OrdersMaterialized != 0 || firstReport.OrdersExternal != 1 || firstReport.FillsApplied != 1 || firstReport.CursorsCommitted != 0 {
		t.Fatalf("first report=%+v, want one hydrated/applied fill and no committed cursor", firstReport)
	}
	if firstCallbacks != 1 {
		t.Fatalf("first callbacks=%d, want 1", firstCallbacks)
	}
	firstOrder, ok := first.Cache.OrderForAccount("acct", fill.ClientID)
	if !ok || firstOrder.Status != enums.StatusFilled || !firstOrder.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("first materialized order=(%+v,%v), want filled qty 1", firstOrder, ok)
	}
	if qty := first.Portfolio.NetQtyForAccount("acct", id, enums.PosLong); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("first portfolio qty=%s, want 1", qty)
	}
	if got := journalRecordCount(underlying, journal.RecordAppliedEvent); got != 1 {
		t.Fatalf("applied-event records after cursor failure=%d, want 1", got)
	}
	if got := journalRecordCount(underlying, journal.RecordReconciliationCursor); got != 0 {
		t.Fatalf("cursor records after injected failure=%d, want zero", got)
	}

	secondCallbacks := 0
	second := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass, order)}, clock.NewSimulatedClock(mass.GeneratedAt), "failed-cursor",
		WithJournal(underlying), WithOnFill(func(model.Fill) { secondCallbacks++ }))
	report, err := second.Resync(context.Background())
	if err != nil {
		t.Fatalf("second resync: %v", err)
	}
	if secondCallbacks != 0 || report.OrdersMaterialized != 0 || report.FillsApplied != 0 || report.FillsDuplicate != 1 || report.CursorsCommitted != 1 {
		t.Fatalf("second callbacks=%d report=%+v, durable fill was re-applied after cursor failure", secondCallbacks, report)
	}
	if got := journalRecordCount(underlying, journal.RecordAppliedEvent); got != 1 {
		t.Fatalf("applied-event records after restart retry=%d, want 1", got)
	}
	if got := journalRecordCount(underlying, journal.RecordReconciliationCursor); got != 1 {
		t.Fatalf("cursor records after restart retry=%d, want 1", got)
	}
}

func TestRecoveredFillsApplyInVenueTimestampOrder(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 30, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "XRP-USDT", Kind: enums.KindPerp}
	for attempt := 0; attempt < 40; attempt++ {
		orders := []model.Order{
			recoveryOrder(id, "buy-100", "venue-buy-100", "1", at),
			recoveryOrder(id, "buy-200", "venue-buy-200", "1", at),
			recoveryOrder(id, "sell-150", "venue-sell-150", "1", at),
		}
		orders[2].Request.Side = enums.SideSell
		fills := []model.Fill{
			{AccountID: "acct", InstrumentID: id, ClientID: "buy-100", VenueOrderID: "venue-buy-100", TradeID: "buy-100", Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at},
			{AccountID: "acct", InstrumentID: id, ClientID: "buy-200", VenueOrderID: "venue-buy-200", TradeID: "buy-200", Side: enums.SideBuy, Price: decimal.NewFromInt(200), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(time.Second)},
			{AccountID: "acct", InstrumentID: id, ClientID: "sell-150", VenueOrderID: "venue-sell-150", TradeID: "sell-150", Side: enums.SideSell, Price: decimal.NewFromInt(150), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(2 * time.Second)},
		}
		mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
		for _, fill := range fills {
			if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
				t.Fatalf("add fill report: %v", err)
			}
		}
		node := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "ordered")
		for _, order := range orders {
			node.Cache.UpsertOrder(order)
		}
		if _, err := node.Resync(context.Background()); err != nil {
			t.Fatalf("attempt %d resync: %v", attempt, err)
		}
		pnl := node.Portfolio.RealizedPnLForAccount("acct")
		avg := node.Portfolio.AvgPriceForAccount("acct", id, enums.PosNet)
		if !pnl.IsZero() || !avg.Equal(decimal.NewFromInt(150)) {
			t.Fatalf("attempt %d realized=%s avg=%s, want venue-time result 0/150", attempt, pnl, avg)
		}
	}
}

type startupOrderObserver struct {
	observ.Base
	events chan string
}

func (o *startupOrderObserver) OnNodeStart()      { o.events <- "start" }
func (o *startupOrderObserver) OnFill(model.Fill) { o.events <- "fill" }

func TestStartupObserverReceivesFillAfterNodeStart(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 40, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "ADA-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "observer-client", "observer-venue", "1", at)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "observer-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	observer := &startupOrderObserver{events: make(chan string, 2)}
	node := NewNode(Clients{Execution: newRecoveredFillHistoryExec(mass)}, clock.NewSimulatedClock(mass.GeneratedAt), "observer", WithObserver(observer))
	node.Cache.UpsertOrder(order)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()

	var got []string
	for len(got) < 2 {
		select {
		case event := <-observer.events:
			got = append(got, event)
		case <-time.After(time.Second):
			cancel()
			t.Fatalf("observer events=%v, want [start fill]", got)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
	if got[0] != "start" || got[1] != "fill" {
		t.Fatalf("observer events=%v, want [start fill]", got)
	}
}
