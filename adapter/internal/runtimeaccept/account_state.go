package runtimeaccept

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/shopspring/decimal"
)

// AttachAccountRequiredRiskWithMaxNotional installs the normal account gate
// plus the caller's venue-specific acceptance notional envelope.
func AttachAccountRequiredRiskWithMaxNotional(node *btruntime.TradingNode, provider model.InstrumentProvider, maxNotional decimal.Decimal) {
	riskEngine := risk.New(risk.Limits{MaxOrderNotional: maxNotional}, node.Cache).
		WithInstrumentProvider(provider).
		RequireAccountState()
	riskEngine.SetRuntimeCapabilities(node.ExecutionCapabilities(), node.AccountCapabilities())
	btruntime.WithRisk(riskEngine, provider)(node)
}

func AssertAccountStateReady(t testing.TB, node *btruntime.TradingNode, accountID string, typ model.AccountType, kind enums.InstrumentKind) {
	t.Helper()
	acct, ok := node.Cache.Account(accountID)
	if !ok {
		t.Fatalf("runtime cache missing account state for %s", accountID)
	}
	if acct.Type() != typ {
		t.Fatalf("runtime account %s type=%s, want %s", accountID, acct.Type(), typ)
	}
	state := acct.LastEvent()
	if err := state.Validate(); err != nil {
		t.Fatalf("runtime account %s state invalid: %v", accountID, err)
	}
	if state.AccountID != accountID {
		t.Fatalf("runtime account state account_id=%q, want %q", state.AccountID, accountID)
	}
	if kind == enums.KindUnknown {
		t.Fatalf("runtime account %s requires a concrete product kind", accountID)
	}
	if err := runtimeProductSupportReady(node.ExecutionCapabilities(), node.AccountCapabilities(), node.Exec != nil, kind); err != nil {
		t.Fatalf("runtime account %s product %s support incomplete: %v", accountID, kind, err)
	}
	if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("runtime account %s state envelope incomplete: %+v", accountID, state)
	}
	freshness := acct.Freshness()
	metrics := node.Metrics()
	if freshness.LastReconciledAt.IsZero() {
		t.Fatalf("runtime account %s has no initial authoritative reconciliation: %+v", accountID, freshness)
	}
	if freshness.StaleAfter <= 0 || time.Duration(metrics.AccountStateAgeNs) > freshness.StaleAfter {
		t.Fatalf("runtime account %s is not fresh after reconcile: freshness=%+v age=%s", accountID, freshness, time.Duration(metrics.AccountStateAgeNs))
	}
	if len(acct.Balances()) == 0 {
		t.Fatalf("runtime account %s has no balances", accountID)
	}
	for _, balance := range acct.Balances() {
		if balance.AccountID != accountID {
			t.Fatalf("runtime account balance %s account_id=%q, want %q", balance.Currency, balance.AccountID, accountID)
		}
		cached, ok := node.Cache.BalanceForAccount(accountID, balance.Currency)
		if !ok {
			t.Fatalf("runtime cache missing account-scoped balance %s/%s", accountID, balance.Currency)
		}
		if cached.AccountID != accountID {
			t.Fatalf("runtime cache balance %s account_id=%q, want %q", balance.Currency, cached.AccountID, accountID)
		}
	}
	if _, ok := node.Portfolio.Account(accountID); !ok {
		t.Fatalf("runtime portfolio cannot read account %s", accountID)
	}
	if equity, ok := node.Portfolio.Equity(accountID); !ok || len(equity) == 0 {
		t.Fatalf("runtime portfolio equity=%v ok=%v for account %s, want non-empty", equity, ok, accountID)
	}
	if typ == model.AccountMargin && kind != enums.KindSpot {
		if initial, ok := node.Portfolio.MarginInitial(accountID); !ok {
			t.Fatalf("runtime portfolio initial margin=%v ok=%v for account %s, want readable account", initial, ok, accountID)
		}
		if maintenance, ok := node.Portfolio.MarginMaintenance(accountID); !ok {
			t.Fatalf("runtime portfolio maintenance margin=%v ok=%v for account %s, want readable account", maintenance, ok, accountID)
		}
	}
	if metrics.Accounts == 0 || metrics.AccountStateAgeNs < 0 {
		t.Fatalf("runtime metrics did not expose account state: %+v", metrics)
	}
	health := node.Health()
	if health.Accounts == 0 || health.AccountStateAgeNs < 0 {
		t.Fatalf("runtime health did not expose account state: %+v", health)
	}
}

func runtimeProductSupportReady(execution, account *contract.Capabilities, requireTrading bool, kind enums.InstrumentKind) error {
	if kind == enums.KindUnknown {
		return fmt.Errorf("product kind is required")
	}
	trading := false
	accountState := false
	if execution != nil {
		for _, product := range execution.Products {
			if product.Kind != kind {
				continue
			}
			if product.Trading && execution.Trading.Submit {
				trading = true
			}
		}
	}
	if account != nil {
		for _, product := range account.Products {
			if product.Kind == kind && product.Account {
				accountState = true
			}
		}
	}
	if requireTrading && !trading {
		return fmt.Errorf("missing trading submit capability for %s", kind)
	}
	if !accountState {
		return fmt.Errorf("missing account-state capability for %s", kind)
	}
	return nil
}

func AssertOversizedOrderRejected(t testing.TB, node *btruntime.TradingNode, provider model.InstrumentProvider, id model.InstrumentID, maxNotional decimal.Decimal) {
	t.Helper()
	if node == nil || node.Exec == nil {
		t.Fatal("runtime oversized-order probe requires a configured execution engine")
	}
	if provider == nil {
		t.Fatal("runtime oversized-order probe requires an instrument provider")
	}
	if !maxNotional.IsPositive() {
		t.Fatalf("runtime oversized-order probe max notional=%s, want positive", maxNotional)
	}
	inst, ok := provider.Instrument(id)
	if !ok || inst == nil {
		t.Fatalf("instrument provider missing %s", id)
	}
	qty := inst.MinQty
	if !qty.IsPositive() {
		qty = inst.SizeStep
	}
	if !qty.IsPositive() {
		qty = decimal.NewFromInt(1)
	}
	price := oversizedOrderProbePrice(inst, qty, maxNotional)
	_, err := node.Exec.Submit(context.Background(), model.OrderRequest{
		ClientID:     "runtime-risk-probe",
		InstrumentID: id,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("runtime oversized-order probe err=%v, want ErrRiskRejected", err)
	}
}

func oversizedOrderProbePrice(inst *model.Instrument, qty, maxNotional decimal.Decimal) decimal.Decimal {
	multiplier := decimal.NewFromInt(1)
	if inst != nil && inst.ContractMultiplier.IsPositive() {
		multiplier = inst.ContractMultiplier
	}
	price := maxNotional.Mul(decimal.NewFromInt(2)).Div(qty).Div(multiplier)
	if inst != nil && inst.PriceTick.IsPositive() {
		price = price.Div(inst.PriceTick).Ceil().Mul(inst.PriceTick)
	}
	return price
}

func WaitForActive(ctx context.Context, node *btruntime.TradingNode) error {
	var last lifecycle.Snapshot
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.State()
		if last.Node == lifecycle.NodeRunning && last.Trading == lifecycle.TradingActive {
			return nil
		}
		if last.Node == lifecycle.NodeFailed || last.Node == lifecycle.NodeStopped {
			return fmt.Errorf("runtime stopped before becoming active; last=%+v", last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime active; last=%+v: %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func WaitForOrderFilled(ctx context.Context, node *btruntime.TradingNode, clientID string) error {
	var last enums.OrderStatus
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if order, ok := node.Cache.Order(clientID); ok {
			last = order.Status
			if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime order %s filled; last=%v: %w", clientID, last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func WaitForOrderStatus(ctx context.Context, node *btruntime.TradingNode, clientID string, status enums.OrderStatus) error {
	var last enums.OrderStatus
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if order, ok := node.Cache.Order(clientID); ok {
			last = order.Status
			if order.Status == status {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime order %s status %s; last=%v: %w", clientID, status, last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func WaitForPortfolioNetQty(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, minAbs decimal.Decimal) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if last.Abs().GreaterThanOrEqual(minAbs.Abs()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime portfolio net qty >= %s; last=%s: %w", minAbs.Abs(), last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func WaitForPortfolioFlat(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, tolerance decimal.Decimal) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if tolerance.IsPositive() {
			if last.Abs().LessThan(tolerance.Abs()) {
				return nil
			}
		} else if last.IsZero() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime portfolio flat within %s; last=%s: %w", tolerance.Abs(), last, ctx.Err())
		case <-ticker.C:
		}
	}
}
