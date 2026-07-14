package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestGateTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetRead(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newGateAcceptanceAdapter(t, ctx, cfg, []string{gatesdk.ProductSpot, gatesdk.ProductFuturesUSDT})
	defer adapter.Close()
	for _, candidate := range []struct {
		label  string
		symbol string
		kind   enums.InstrumentKind
		settle string
	}{
		{label: "Spot", symbol: cfg.SpotSymbol, kind: enums.KindSpot},
		{label: "USDT Perp", symbol: cfg.USDTPerpSymbol, kind: enums.KindPerp, settle: "USDT"},
	} {
		id := requireGateAcceptanceInstrument(t, adapter, candidate.symbol, candidate.kind, candidate.settle)
		book, err := adapter.Market.OrderBook(ctx, id, 5)
		if err != nil {
			testenv.SkipIfTransientLiveNetworkError(t, err, fmt.Sprintf("Gate Testnet %s order book", candidate.label))
			t.Fatalf("Gate Testnet %s order book: %v", candidate.label, err)
		}
		if len(book.Bids) == 0 || len(book.Asks) == 0 {
			t.Fatalf("Gate Testnet %s empty book for %s: %+v", candidate.label, candidate.symbol, book)
		}
	}
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Gate Testnet account state")
		t.Fatalf("Gate Testnet account state: %v", err)
	}
	if state.AccountID != AccountIDUnified || state.Venue != VenueName {
		t.Fatalf("Gate Testnet account identity mismatch: %+v", state)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("Gate Testnet account state invalid: %v", err)
	}
	futuresAccount, err := adapter.rest.GetFuturesAccount(ctx, gatesdk.SettleUSDT)
	if err != nil {
		t.Fatalf("Gate Testnet futures account mode: %v", err)
	}
	t.Logf("Gate Testnet futures_position_mode=%s in_dual_mode=%t", futuresAccount.PositionMode, futuresAccount.InDualMode)
}

func TestGateTestnetSpotAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetWrite(t)
	runGateAcceptance(t, "Gate Testnet Spot", cfg, cfg.SpotSymbol, enums.KindSpot, "", []string{gatesdk.ProductSpot})
}

func TestGateTestnetUSDTPerpAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetWrite(t)
	runGateAcceptance(t, "Gate Testnet USDT Perp", cfg, cfg.USDTPerpSymbol, enums.KindPerp, "USDT", []string{gatesdk.ProductFuturesUSDT})
}

func TestGateTestnetSpotRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetWrite(t)
	runGateRuntimeAcceptance(t, "Gate Testnet Spot Runtime", cfg, cfg.SpotSymbol, enums.KindSpot, "", []string{gatesdk.ProductSpot}, model.AccountCash)
}

func TestGateTestnetUSDTPerpRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetWrite(t)
	runGateRuntimeAcceptance(t, "Gate Testnet USDT Perp Runtime", cfg, cfg.USDTPerpSymbol, enums.KindPerp, "USDT", []string{gatesdk.ProductFuturesUSDT}, model.AccountMargin)
}

func runGateAcceptance(t *testing.T, label string, cfg testenv.GateTestnetConfig, symbol string, kind enums.InstrumentKind, settle string, products []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newGateAcceptanceAdapter(t, ctx, cfg, products)
	defer adapter.Close()
	id := requireGateAcceptanceInstrument(t, adapter, symbol, kind, settle)
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" order book")
		t.Fatalf("%s order book: %v", label, err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty book for %s: %+v", label, symbol, book)
	}
	lifecycle := gateAcceptanceLifecycleSpec(t, adapter, label, id, book, cfg.MaxNotionalUSDT)
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" account state")
		t.Fatalf("%s account state: %v", label, err)
	}
	if state.AccountID != AccountIDUnified {
		t.Fatalf("%s account id=%q, want %q", label, state.AccountID, AccountIDUnified)
	}
	ensureGateLifecycleFunds(t, label, adapter, state, lifecycle)
	if _, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("%s adapter order lifecycle: %v", label, err)
	}
}

func runGateRuntimeAcceptance(t *testing.T, label string, cfg testenv.GateTestnetConfig, symbol string, kind enums.InstrumentKind, settle string, products []string, accountType model.AccountType) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newGateAcceptanceAdapter(t, ctx, cfg, products)
	defer adapter.Close()
	if kind == enums.KindSpot && adapter.privateSpot != nil {
		adapter.privateSpot = &gateAcceptancePrivateStreamProbe{privateStreamClient: adapter.privateSpot, t: t}
	} else if kind == enums.KindPerp && adapter.privateFutures != nil {
		adapter.privateFutures = &gateAcceptancePrivateStreamProbe{privateStreamClient: adapter.privateFutures, t: t}
		account, err := adapter.rest.GetFuturesAccount(ctx, gatesdk.SettleUSDT)
		if err != nil {
			t.Fatalf("%s futures account mode: %v", label, err)
		}
		t.Logf("%s futures_position_mode=%s in_dual_mode=%t", label, account.PositionMode, account.InDualMode)
	}
	id := requireGateAcceptanceInstrument(t, adapter, symbol, kind, settle)
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" order book")
		t.Fatalf("%s order book: %v", label, err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty book for %s: %+v", label, symbol, book)
	}
	lifecycle := gateAcceptanceLifecycleSpec(t, adapter, label, id, book, cfg.MaxNotionalUSDT)
	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		AccountIDUnified,
		btruntime.WithAccountID(AccountIDUnified),
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT)
	report, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" initial reconcile")
		t.Fatalf("%s initial reconcile: %v", label, err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("%s account states applied=%d, want 1: %+v", label, report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, accountType, kind)
	runtimeaccept.AssertOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), id)
	if state, ok := node.Cache.Account(AccountIDUnified); ok {
		ensureGateLifecycleFunds(t, label, adapter, state.LastEvent(), lifecycle)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" private stream")
		t.Fatalf("%s private stream: %v", label, err)
	}
	lifecycle.PrivateStreamTopics = gatePrivateStreamTopics(kind)
	t.Logf("%s private_stream_topics=%s", label, strings.Join(lifecycle.PrivateStreamTopics, ","))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer stopGateRuntimeNode(t, stop, done)
	if _, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("%s runtime order lifecycle: %v", label, err)
	}
	finalReport, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" final reconcile")
		t.Fatalf("%s final reconcile: %v", label, err)
	}
	if finalReport.AccountStatesApplied != 1 {
		t.Fatalf("%s final account states applied=%d, want 1: %+v", label, finalReport.AccountStatesApplied, finalReport)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, accountType, kind)
}

type gateAcceptancePrivateStreamProbe struct {
	privateStreamClient
	t *testing.T
}

func (p *gateAcceptancePrivateStreamProbe) Subscribe(ctx context.Context, channel string, payload []string, handler func(json.RawMessage)) error {
	return p.privateStreamClient.Subscribe(ctx, channel, payload, func(raw json.RawMessage) {
		if p.t != nil {
			switch channel {
			case gatesdk.ChannelSpotOrder:
				msg, err := gatesdk.DecodeSpotOrderMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Orders))
				}
			case gatesdk.ChannelSpotUserTrade:
				msg, err := gatesdk.DecodeSpotUserTradeMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Trades))
				}
			case gatesdk.ChannelSpotBalance:
				msg, err := gatesdk.DecodeSpotBalanceMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Balances))
				}
			case gatesdk.ChannelFuturesOrder:
				msg, err := gatesdk.DecodeFuturesOrderMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Orders))
				}
			case gatesdk.ChannelFuturesUserTrade:
				msg, err := gatesdk.DecodeFuturesUserTradeMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Trades))
				}
			case gatesdk.ChannelFuturesPosition:
				msg, err := gatesdk.DecodeFuturesPositionMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Positions))
				}
			case gatesdk.ChannelFuturesBalance:
				msg, err := gatesdk.DecodeFuturesBalanceMessage(raw)
				if err != nil {
					p.t.Logf("gate_private_event channel=%s decode_error=%v", channel, err)
				} else {
					p.t.Logf("gate_private_event channel=%s event=%s records=%d", channel, msg.Event, len(msg.Balances))
				}
			}
		}
		handler(raw)
	})
}

func newGateAcceptanceAdapter(t *testing.T, ctx context.Context, cfg testenv.GateTestnetConfig, products []string) *Adapter {
	t.Helper()
	httpClient, err := testenv.GateTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Gate HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		APIKey:    cfg.APIKey,
		APISecret: cfg.APISecret,
		Environment: gatesdk.EnvironmentProfile{
			RESTBaseURL:      cfg.Profile.RESTBaseURL,
			SpotWSURL:        cfg.Profile.SpotWSURL,
			FuturesUSDTWSURL: cfg.Profile.FuturesUSDTWSURL,
			OfficialTestnet:  cfg.Profile.OfficialTestnet,
		},
		HTTPClient: httpClient,
		Products:   products,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Gate adapter initialization")
		t.Fatalf("new Gate adapter: %v", err)
	}
	return adapter
}

func requireGateAcceptanceInstrument(t *testing.T, adapter *Adapter, venueSymbol string, kind enums.InstrumentKind, settle string) model.InstrumentID {
	t.Helper()
	id, ok := adapter.provider.ResolveVenueInstrument(venueSymbol, kind, settle)
	if !ok {
		t.Fatalf("Gate symbol %s kind=%s settle=%q not loaded", venueSymbol, kind, settle)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("Gate instrument %s not available", id)
	}
	if inst.ID.Kind != kind {
		t.Fatalf("Gate instrument %s kind=%s, want %s", id, inst.ID.Kind, kind)
	}
	if settle != "" && inst.Settle != settle {
		t.Fatalf("Gate instrument %s settle=%q, want %q", id, inst.Settle, settle)
	}
	return id
}

func gateAcceptanceLifecycleSpec(t *testing.T, adapter *Adapter, label string, id model.InstrumentID, book *model.OrderBook, maxNotional decimal.Decimal) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty order book for lifecycle: %+v", label, book)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("%s instrument %s not available", label, id)
	}
	restingPrice := floorGateAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	fillPrice := ceilGateAcceptanceDecimal(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), inst.PriceTick)
	closePrice := floorGateAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	qty := gateAcceptanceQuantity(t, label, inst, maxNotional, minPositiveGateDecimal(restingPrice, fillPrice, closePrice), maxPositiveGateDecimal(restingPrice, fillPrice, closePrice))
	closeQty := decimal.Zero
	if id.Kind == enums.KindSpot {
		closeQty = gateAcceptanceSpotCloseQuantity(t, label, inst, qty)
	}
	spec := runtimeaccept.OrderLifecycleSpec{
		Label:          label,
		Venue:          VenueName,
		Environment:    "Testnet",
		Product:        gateAcceptanceProduct(inst.ID.Kind, inst.Settle),
		AccountID:      AccountIDUnified,
		InstrumentID:   id,
		Quantity:       qty,
		CloseQuantity:  closeQty,
		RestingPrice:   restingPrice,
		FillPrice:      fillPrice,
		ClosePrice:     closePrice,
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		Logf:           t.Logf,
	}
	if id.Kind == enums.KindSpot {
		step := inst.SizeStep
		if !step.IsPositive() {
			step = decimal.NewFromInt(1)
		}
		minQty := inst.MinQty
		if !minQty.IsPositive() {
			minQty = step
		}
		spec = runtimeaccept.ConfigureSpotBalanceGuard(spec, adapter.acct, inst.Base, step, minQty, inst.MinNotional, qty.Sub(closeQty))
	}
	return spec
}

func gateAcceptanceSpotCloseQuantity(t *testing.T, label string, inst *model.Instrument, qty decimal.Decimal) decimal.Decimal {
	t.Helper()
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	closeQty := floorGateAcceptanceDecimal(qty.Mul(gateSpotCloseQuantityBuffer()), step)
	if closeQty.LessThan(minQty) {
		t.Skipf("skipping %s: spot close quantity %s is below min quantity %s after fee buffer", label, closeQty, minQty)
	}
	return closeQty
}

func gateSpotCloseQuantityBuffer() decimal.Decimal {
	return decimal.RequireFromString("0.995")
}

func gateAcceptanceProduct(kind enums.InstrumentKind, settle string) string {
	if kind == enums.KindSpot {
		return "Spot cash"
	}
	if settle == "USDT" {
		return "USDT-linear Perp/SWAP"
	}
	return "Linear Perp/SWAP"
}

func gatePrivateStreamTopics(kind enums.InstrumentKind) []string {
	if kind == enums.KindSpot {
		return []string{gatesdk.ChannelSpotOrder, gatesdk.ChannelSpotUserTrade, gatesdk.ChannelSpotBalance}
	}
	return []string{gatesdk.ChannelFuturesOrder, gatesdk.ChannelFuturesUserTrade, gatesdk.ChannelFuturesPosition, gatesdk.ChannelFuturesBalance}
}

func gateAcceptanceQuantity(t *testing.T, label string, inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) decimal.Decimal {
	t.Helper()
	if !maxNotional.IsPositive() {
		t.Fatalf("%s max notional must be positive, got %s", label, maxNotional)
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	multiplier := gateContractMultiplier(inst)
	qty := inst.MinQty
	if !qty.IsPositive() {
		qty = step
	}
	if inst.ID.Kind == enums.KindSpot {
		minBufferedQty := qty.Div(gateSpotCloseQuantityBuffer())
		if minBufferedQty.GreaterThan(qty) {
			qty = minBufferedQty
		}
	}
	if inst.MinNotional.IsPositive() && minNotionalPrice.IsPositive() {
		minByNotional := inst.MinNotional.Div(minNotionalPrice.Mul(multiplier))
		if minByNotional.GreaterThan(qty) {
			qty = minByNotional
		}
		if inst.ID.Kind == enums.KindSpot {
			minCloseQty := ceilGateAcceptanceDecimal(minByNotional, step)
			minBufferedQty := minCloseQty.Div(gateSpotCloseQuantityBuffer())
			if minBufferedQty.GreaterThan(qty) {
				qty = minBufferedQty
			}
		}
	}
	qty = ceilGateAcceptanceDecimal(qty, step)
	if maxNotionalPrice.IsPositive() && qty.Mul(maxNotionalPrice).Mul(multiplier).GreaterThan(maxNotional) {
		t.Skipf("skipping %s: min lifecycle quantity %s notional %s exceeds max notional %s", label, qty, qty.Mul(maxNotionalPrice).Mul(multiplier), maxNotional)
	}
	return qty
}

func ensureGateLifecycleFunds(t *testing.T, label string, adapter *Adapter, state model.AccountState, spec runtimeaccept.OrderLifecycleSpec) {
	t.Helper()
	inst, ok := adapter.provider.Instrument(spec.InstrumentID)
	if !ok {
		t.Fatalf("%s instrument %s not available", label, spec.InstrumentID)
	}
	required := spec.Quantity.Mul(spec.FillPrice).Mul(gateContractMultiplier(inst))
	currency := "USDT"
	if spec.InstrumentID.Kind == enums.KindSpot {
		currency = inst.Quote
	} else if inst.Settle != "" {
		currency = inst.Settle
	}
	for _, balance := range state.Balances {
		if balance.Currency == currency && balance.FreeOrAvailable().GreaterThanOrEqual(required) {
			return
		}
	}
	t.Skipf("skipping %s: no %s balance with available >= %s for lifecycle", label, currency, required)
}

func gateContractMultiplier(inst *model.Instrument) decimal.Decimal {
	if inst != nil && inst.ContractMultiplier.IsPositive() {
		return inst.ContractMultiplier
	}
	return decimal.NewFromInt(1)
}

func minPositiveGateDecimal(values ...decimal.Decimal) decimal.Decimal {
	out := decimal.Zero
	for _, value := range values {
		if !value.IsPositive() {
			continue
		}
		if out.IsZero() || value.LessThan(out) {
			out = value
		}
	}
	return out
}

func maxPositiveGateDecimal(values ...decimal.Decimal) decimal.Decimal {
	out := decimal.Zero
	for _, value := range values {
		if value.GreaterThan(out) {
			out = value
		}
	}
	return out
}

func ceilGateAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorGateAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func stopGateRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Gate runtime node did not stop")
	}
}
