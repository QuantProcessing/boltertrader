package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/reconcile"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

func TestLiveFillIdentityConflictHaltsWithoutMutation(t *testing.T) {
	btc := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	eth := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	tests := []struct {
		name string
		fill model.Fill
	}{
		{
			name: "wrong instrument",
			fill: model.Fill{AccountID: "acct", InstrumentID: eth, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideBuy},
		},
		{
			name: "partial instrument is not missing",
			fill: model.Fill{AccountID: "acct", InstrumentID: model.InstrumentID{Venue: "OTHER"}, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideBuy},
		},
		{
			name: "wrong side",
			fill: model.Fill{AccountID: "acct", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideSell},
		},
		{
			name: "crossed aliases",
			fill: model.Fill{AccountID: "acct", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-b", Side: enums.SideBuy},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := newFillIdentityTestNode(t, btc)
			fill := tt.fill
			fill.TradeID = "conflict-trade"
			fill.Price = decimal.NewFromInt(100)
			fill.Quantity = decimal.NewFromInt(1)
			fill.Timestamp = time.Unix(100, 0)
			node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: fill}))

			if state := node.State(); state.Trading != lifecycle.TradingHalted || !strings.Contains(state.Reason, "fill order identity conflict") {
				t.Fatalf("state=%+v, want fail-closed identity halt", state)
			}
			for _, clientID := range []string{"client-a", "client-b"} {
				order, ok := node.Cache.OrderForAccount("acct", clientID)
				if !ok || !order.FilledQty.IsZero() {
					t.Fatalf("order %s=(%+v,%v), want unchanged", clientID, order, ok)
				}
			}
			if got := node.Portfolio.NetQtyForAccount("acct", btc, enums.PosNet); !got.IsZero() {
				t.Fatalf("BTC portfolio qty=%s, want 0", got)
			}
			if got := node.Portfolio.NetQtyForAccount("acct", eth, enums.PosNet); !got.IsZero() {
				t.Fatalf("ETH portfolio qty=%s, want 0", got)
			}
			if got := node.Metrics().FillsSeen; got != 0 {
				t.Fatalf("fills seen=%d, want 0", got)
			}
			if got := node.fills.Count(); got != 0 {
				t.Fatalf("buffered conflicting fills=%d, want 0", got)
			}
		})
	}
}

func TestLiveVenueOnlyFillIgnoresClientNamespaceCollision(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	node := NewNode(Clients{}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	clientCollision := fillIdentityOrder(id, "shared", "venue-a")
	venueMatch := fillIdentityOrder(id, "client-b", "shared")
	node.Cache.UpsertOrder(clientCollision)
	node.Cache.UpsertOrder(venueMatch)

	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		VenueOrderID: "shared",
		TradeID:      "typed-namespace-trade",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Unix(100, 0),
	}}))

	first, _ := node.Cache.OrderForAccount("acct", clientCollision.Request.ClientID)
	second, _ := node.Cache.OrderForAccount("acct", venueMatch.Request.ClientID)
	if !first.FilledQty.IsZero() || !second.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("client-collision qty=%s venue-match qty=%s, want 0/1", first.FilledQty, second.FilledQty)
	}
	if state := node.State(); state.Trading == lifecycle.TradingHalted {
		t.Fatalf("valid typed venue match halted node: %+v", state)
	}
}

func TestLiveFillPersistsNewVenueAliasBeforeLaterVenueOnlyFill(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	node := NewNode(Clients{}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	order := fillIdentityOrder(id, "client-a", "")
	node.Cache.UpsertOrder(order)

	emit := func(clientID, tradeID string) {
		node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: "venue-learned",
			TradeID: tradeID, Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(100, 0),
		}}))
	}
	emit("client-a", "alias-1")
	emit("", "alias-2")

	got, ok := node.Cache.OrderForAccount("acct", "client-a")
	if !ok || got.VenueOrderID != "venue-learned" || !got.FilledQty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("order=(%+v,%v), want one enriched order filled to 2", got, ok)
	}
	if orders := node.Cache.Orders(); len(orders) != 1 {
		t.Fatalf("orders=%+v, want no venue-only materialized duplicate", orders)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("portfolio qty=%s, want 2", qty)
	}
}

func TestActiveFillCoverageSurvivesUnrelatedTradeIDEviction(t *testing.T) {
	btc := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	eth := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	node := NewNode(Clients{}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	node.fills = exec.NewFillBufferWithAppliedLimit(2)
	node.Cache.UpsertOrder(fillIdentityOrder(btc, "client-a", "venue-a"))
	node.Cache.UpsertOrder(fillIdentityOrder(eth, "client-b", "venue-b"))

	emit := func(id model.InstrumentID, clientID, venueOrderID, tradeID string) {
		node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueOrderID,
			TradeID: tradeID, Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(100, 0),
		}}))
	}
	emit(btc, "client-a", "venue-a", "a-1")
	emit(btc, "client-a", "venue-a", "a-2")
	emit(eth, "client-b", "venue-b", "b-1") // globally evicts a-1
	emit(btc, "client-a", "venue-a", "a-3")

	order, _ := node.Cache.OrderForAccount("acct", "client-a")
	portfolioQty := node.Portfolio.NetQtyForAccount("acct", btc, enums.PosNet)
	if !order.FilledQty.Equal(decimal.NewFromInt(3)) || !portfolioQty.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("order qty=%s portfolio qty=%s, want 3/3 after unrelated FIFO eviction", order.FilledQty, portfolioQty)
	}
}

func TestFillBufferConflictDoesNotPersistCacheAlias(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	node := NewNode(Clients{}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	order := fillIdentityOrder(id, "client-a", "")
	node.Cache.UpsertOrder(order)
	if applied, _, err := node.fills.MarkAppliedWithCoverageChecked(model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-a", VenueOrderID: "venue-a",
		TradeID: "seed-alias", Side: enums.SideBuy, Quantity: decimal.NewFromInt(1),
	}); err != nil || !applied {
		t.Fatalf("seed fill-buffer alias applied=%v err=%v", applied, err)
	}

	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-a", VenueOrderID: "venue-b",
		TradeID: "conflicting-alias", Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(101, 0),
	}}))

	got, ok := node.Cache.OrderByClientIDForAccount("acct", "client-a")
	if !ok || got.VenueOrderID != "" || !got.FilledQty.IsZero() {
		t.Fatalf("cache order=(%+v,%v), fill-buffer conflict persisted identity or quantity", got, ok)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.IsZero() {
		t.Fatalf("portfolio qty=%s, want zero", qty)
	}
	if state := node.State(); state.Trading != lifecycle.TradingHalted {
		t.Fatalf("state=%+v, want identity halt", state)
	}
}

func TestLiveTradeIDReuseAcrossOrdersHaltsWithoutMutatingSecondOrder(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	node := newFillIdentityTestNode(t, id)
	emit := func(clientID, venueOrderID string) {
		node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueOrderID,
			TradeID: "reused-trade", Side: enums.SideBuy, Price: decimal.NewFromInt(100),
			Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(101, 0),
		}}))
	}

	emit("client-a", "venue-a")
	emit("client-b", "venue-b")

	first, _ := node.Cache.OrderByClientIDForAccount("acct", "client-a")
	second, _ := node.Cache.OrderByClientIDForAccount("acct", "client-b")
	if !first.FilledQty.Equal(decimal.NewFromInt(1)) || !second.FilledQty.IsZero() {
		t.Fatalf("first qty=%s second qty=%s, want 1/0", first.FilledQty, second.FilledQty)
	}
	if state := node.State(); state.Trading != lifecycle.TradingHalted || !strings.Contains(state.Reason, "fill order identity conflict") {
		t.Fatalf("state=%+v, want fail-closed trade identity halt", state)
	}
}

func TestLiveCrossedOrderAliasesHaltWithoutResolvingInFlight(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fake := runtimetest.NewFakeExec()
	node := NewNode(Clients{Execution: fake}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	first := fillIdentityOrder(id, "client-a", "venue-a")
	second := fillIdentityOrder(id, "client-b", "venue-b")
	node.Cache.UpsertOrder(first)
	node.Cache.UpsertOrder(second)
	inflight := exec.NewInFlightJournal()
	inflight.TrackIntent(journalIntentForIdentityTest(first), exec.InFlightPendingCancel)
	node.Exec.WithInFlightJournal(inflight)

	crossed := first
	crossed.VenueOrderID = second.VenueOrderID
	crossed.UpdatedAt = time.Unix(101, 0)
	node.onExec(contract.NewExecEnvelope(contract.OrderEvent{Order: crossed}))

	if state := node.State(); state.Trading != lifecycle.TradingHalted || !strings.Contains(state.Reason, "order identity conflict") {
		t.Fatalf("state=%+v, want fail-closed order identity halt", state)
	}
	if got := node.Exec.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, crossed order event consumed pending command", got)
	}
	for _, want := range []model.Order{first, second} {
		got, ok := node.Cache.OrderByClientIDForAccount("acct", want.Request.ClientID)
		if !ok || got.VenueOrderID != want.VenueOrderID || got.Status != want.Status {
			t.Fatalf("order %s=(%+v,%v), want unchanged %+v", want.Request.ClientID, got, ok, want)
		}
	}
}

func TestLiveOrderEvidenceMustMatchPendingCommand(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	tests := []struct {
		name       string
		state      exec.InFlightState
		command    journal.CommandType
		intentReq  func(model.OrderRequest) model.OrderRequest
		confirming func(model.Order) model.Order
	}{
		{
			name:    "cancel",
			state:   exec.InFlightPendingCancel,
			command: journal.CommandCancel,
			intentReq: func(req model.OrderRequest) model.OrderRequest {
				return req
			},
			confirming: func(order model.Order) model.Order {
				order.Status = enums.StatusCanceled
				return order
			},
		},
		{
			name:    "modify",
			state:   exec.InFlightPendingModify,
			command: journal.CommandModify,
			intentReq: func(req model.OrderRequest) model.OrderRequest {
				req.Price = decimal.NewFromInt(101)
				req.Quantity = decimal.NewFromInt(2)
				return req
			},
			confirming: func(order model.Order) model.Order {
				order.Request.Price = decimal.NewFromInt(101)
				order.Request.Quantity = decimal.NewFromInt(2)
				return order
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := fillIdentityOrder(id, "client-"+tt.name, "venue-"+tt.name)
			node := NewNode(Clients{Execution: runtimetest.NewFakeExec()}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
			node.Cache.UpsertOrder(order)
			inflight := exec.NewInFlightJournal()
			intentReq := tt.intentReq(order.Request)
			inflight.TrackIntent(journal.CommandIntent{
				RecordID: "intent-" + tt.name, CommandID: "command-" + tt.name, Type: tt.command,
				ClientID: intentReq.ClientID, VenueOrderID: order.VenueOrderID, AccountID: intentReq.AccountID,
				InstrumentID: intentReq.InstrumentID, Side: intentReq.Side, Quantity: intentReq.Quantity, Price: intentReq.Price,
			}, tt.state)
			node.Exec.WithInFlightJournal(inflight)

			unchanged := model.Order{
				Request:      model.OrderRequest{ClientID: order.Request.ClientID},
				VenueOrderID: order.VenueOrderID,
				Status:       enums.StatusNew,
				UpdatedAt:    time.Unix(101, 0),
			}
			node.onExec(contract.NewExecEnvelope(contract.OrderEvent{Order: unchanged}))
			if got := node.Exec.InFlightCount(); got != 1 {
				t.Fatalf("in-flight=%d after unchanged authoritative order, want pending %s", got, tt.name)
			}

			confirmed := tt.confirming(order)
			confirmed.UpdatedAt = time.Unix(102, 0)
			node.onExec(contract.NewExecEnvelope(contract.OrderEvent{Order: confirmed}))
			if got := node.Exec.InFlightCount(); got != 0 {
				t.Fatalf("in-flight=%d after confirming %s evidence, want resolved", got, tt.name)
			}
		})
	}
}

func TestLivePartialFillDoesNotConfirmPendingCancel(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := fillIdentityOrder(id, "client-cancel-fill", "venue-cancel-fill")
	node := NewNode(Clients{Execution: runtimetest.NewFakeExec()}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	node.Cache.UpsertOrder(order)
	inflight := exec.NewInFlightJournal()
	inflight.TrackIntent(journalIntentForIdentityTest(order), exec.InFlightPendingCancel)
	node.Exec.WithInFlightJournal(inflight)

	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID, VenueOrderID: order.VenueOrderID,
		TradeID: "partial-during-cancel", Side: order.Request.Side, Price: order.Request.Price,
		Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(101, 0),
	}}))
	if got := node.Exec.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d after partial fill, want pending cancel", got)
	}

	canceled := order
	canceled.Status = enums.StatusCanceled
	canceled.FilledQty = decimal.NewFromInt(1)
	canceled.UpdatedAt = time.Unix(102, 0)
	node.onExec(contract.NewExecEnvelope(contract.OrderEvent{Order: canceled}))
	if got := node.Exec.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d after canceled order evidence, want resolved", got)
	}
}

func TestCacheCommitFailureDoesNotConsumeFillBufferIdentity(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	node := NewNode(Clients{}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	node.Cache = cache.NewWithTerminalOrderLimit(1)

	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-external", TradeID: "external-trade",
		Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(101, 0),
	}
	materializedClientID := "external-acct-" + fill.VenueOrderID + "-" + fill.TradeID
	collision := fillIdentityOrder(id, materializedClientID, "venue-collision")
	collision.Status = enums.StatusCanceled
	node.Cache.UpsertOrder(collision)

	if got := node.applyFillResult(fill, contract.NewExecEnvelope(contract.FillEvent{Fill: fill})); got != reconcile.FillApplyConflict {
		t.Fatalf("first apply result=%d, want cache identity conflict", got)
	}
	if got := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !got.IsZero() {
		t.Fatalf("portfolio qty=%s after failed cache commit, want zero", got)
	}

	evicting := fillIdentityOrder(id, "evicting-client", "evicting-venue")
	evicting.Status = enums.StatusCanceled
	node.Cache.UpsertOrder(evicting)
	if _, ok := node.Cache.OrderByClientIDForAccount("acct", materializedClientID); ok {
		t.Fatal("terminal client-id collision was not evicted")
	}

	if got := node.applyFillResult(fill, contract.NewExecEnvelope(contract.FillEvent{Fill: fill})); got != reconcile.FillApplyApplied {
		t.Fatalf("replay apply result=%d, want applied after cache collision is removed", got)
	}
	order, ok := node.Cache.OrderByVenueOrderIDForAccount("acct", fill.VenueOrderID)
	if !ok || order.Status != enums.StatusFilled || !order.FilledQty.Equal(fill.Quantity) {
		t.Fatalf("replayed order=(%+v,%v), want filled materialized order", order, ok)
	}
	if got := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !got.Equal(fill.Quantity) {
		t.Fatalf("portfolio qty=%s, want %s after accepted replay", got, fill.Quantity)
	}
}

func TestConflictingFillDoesNotConsumeInFlightRecoveryState(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	wrong := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	fake := runtimetest.NewFakeExec()
	node := NewNode(Clients{Execution: fake}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	order := fillIdentityOrder(id, "client-a", "venue-a")
	node.Cache.UpsertOrder(order)
	inflight := exec.NewInFlightJournal()
	inflight.TrackIntent(journalIntentForIdentityTest(order), exec.InFlightPendingCancel)
	node.Exec.WithInFlightJournal(inflight)

	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
		AccountID: "acct", InstrumentID: wrong, VenueOrderID: "venue-a", TradeID: "wrong-inflight-fill",
		Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(101, 0),
	}}))
	if got := node.Exec.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, conflicting fill consumed recovery state", got)
	}
	if got, _ := node.Cache.OrderByClientIDForAccount("acct", "client-a"); !got.FilledQty.IsZero() {
		t.Fatalf("cache order=%+v, want unchanged", got)
	}
}

func TestDirectClientFillCanMaterializeOnlyAfterValidatedInFlightMatch(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fake := runtimetest.NewFakeExec()
	node := NewNode(Clients{Execution: fake}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	inflight := exec.NewInFlightJournal()
	inflight.TrackIntent(journal.CommandIntent{
		RecordID: "direct-client-intent", CommandID: "direct-client-command", Type: journal.CommandSubmit,
		ClientID: "client-direct", AccountID: "acct", InstrumentID: id, Side: enums.SideBuy, Quantity: decimal.NewFromInt(1),
	}, exec.InFlightSubmitted)
	node.Exec.WithInFlightJournal(inflight)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-direct", VenueOrderID: "venue-direct",
		TradeID: "direct-client-fill", Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: time.Unix(101, 0),
	}
	if got := node.applyFillResult(fill, contract.NewExecEnvelope(contract.FillEvent{Fill: fill})); got != reconcile.FillApplyApplied {
		t.Fatalf("apply result=%d, want applied", got)
	}
	if node.Exec.InFlightCount() != 0 {
		t.Fatalf("in-flight=%d, want resolved after accepted fill", node.Exec.InFlightCount())
	}
	if order, ok := node.Cache.OrderByClientIDForAccount("acct", "client-direct"); !ok || order.VenueOrderID != "venue-direct" || order.Status != enums.StatusFilled {
		t.Fatalf("materialized order=(%+v,%v), want validated direct-client fill", order, ok)
	}
}

func TestConfirmedCancelReleasesTerminalFillCoverage(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fake := runtimetest.NewFakeExec()
	node := NewNode(Clients{Execution: fake}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	node.Exec.WithCommandGate(nil)
	node.fills = exec.NewFillBufferWithAppliedLimit(1)
	order := fillIdentityOrder(id, "client-a", "venue-a")
	node.Cache.UpsertOrder(order)
	old := model.Fill{AccountID: "acct", InstrumentID: id, ClientID: "client-a", VenueOrderID: "venue-a", TradeID: "old", Side: enums.SideBuy, Quantity: decimal.NewFromInt(1)}
	if applied, _, err := node.fills.MarkAppliedWithCoverageChecked(old); err != nil || !applied {
		t.Fatalf("seed coverage applied=%v err=%v", applied, err)
	}
	if err := node.Exec.Cancel(context.Background(), "client-a"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	other := model.Fill{AccountID: "acct", InstrumentID: model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}, VenueOrderID: "other", TradeID: "other", Quantity: decimal.NewFromInt(1)}
	if applied, _, err := node.fills.MarkAppliedWithCoverageChecked(other); err != nil || !applied {
		t.Fatalf("evicting fill applied=%v err=%v", applied, err)
	}
	next := old
	next.TradeID = "next"
	if applied, prior, err := node.fills.MarkAppliedWithCoverageChecked(next); err != nil || !applied || !prior.IsZero() {
		t.Fatalf("post-cancel fill applied=%v prior=%s err=%v, want released terminal coverage", applied, prior, err)
	}
}

func TestConfirmedTerminalModifyReleasesTerminalFillCoverage(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	node := NewNode(Clients{Execution: fake}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	node.Exec.WithCommandGate(nil)
	node.fills = exec.NewFillBufferWithAppliedLimit(1)
	order := fillIdentityOrder(id, "client-a", "venue-a")
	node.Cache.UpsertOrder(order)
	old := model.Fill{AccountID: "acct", InstrumentID: id, ClientID: "client-a", VenueOrderID: "venue-a", TradeID: "old", Side: enums.SideBuy, Quantity: decimal.NewFromInt(1)}
	if applied, _, err := node.fills.MarkAppliedWithCoverageChecked(old); err != nil || !applied {
		t.Fatalf("seed coverage applied=%v err=%v", applied, err)
	}
	fake.SetModifyResult(&model.Order{VenueOrderID: "venue-a", Status: enums.StatusCanceled, UpdatedAt: time.Unix(101, 0)}, nil)
	if _, err := node.Exec.Modify(context.Background(), "client-a", decimal.NewFromInt(101), decimal.NewFromInt(2)); err != nil {
		t.Fatalf("modify: %v", err)
	}
	other := model.Fill{AccountID: "acct", InstrumentID: model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}, VenueOrderID: "other", TradeID: "other", Quantity: decimal.NewFromInt(1)}
	if applied, _, err := node.fills.MarkAppliedWithCoverageChecked(other); err != nil || !applied {
		t.Fatalf("evicting fill applied=%v err=%v", applied, err)
	}
	next := old
	next.TradeID = "next"
	if applied, prior, err := node.fills.MarkAppliedWithCoverageChecked(next); err != nil || !applied || !prior.IsZero() {
		t.Fatalf("post-modify fill applied=%v prior=%s err=%v, want released terminal coverage", applied, prior, err)
	}
}

func journalIntentForIdentityTest(order model.Order) journal.CommandIntent {
	return journal.CommandIntent{
		RecordID: "identity-intent", CommandID: "identity-command", Type: journal.CommandCancel,
		ClientID: order.Request.ClientID, VenueOrderID: order.VenueOrderID, AccountID: order.Request.AccountID,
		InstrumentID: order.Request.InstrumentID, Side: order.Request.Side, Quantity: order.Request.Quantity,
	}
}

func newFillIdentityTestNode(t *testing.T, id model.InstrumentID) *TradingNode {
	t.Helper()
	node := NewNode(Clients{}, clock.NewSimulatedClock(time.Unix(100, 0)), "acct")
	node.Cache.UpsertOrder(fillIdentityOrder(id, "client-a", "venue-a"))
	node.Cache.UpsertOrder(fillIdentityOrder(id, "client-b", "venue-b"))
	return node
}

func fillIdentityOrder(id model.InstrumentID, clientID, venueOrderID string) model.Order {
	return model.Order{
		Request: model.OrderRequest{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, Side: enums.SideBuy,
			Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: decimal.NewFromInt(10), Price: decimal.NewFromInt(100), PositionSide: enums.PosNet,
		},
		VenueOrderID: venueOrderID,
		Status:       enums.StatusNew,
	}
}
