package runtimeaccept

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/shopspring/decimal"
)

func AttachAccountRequiredRisk(node *btruntime.TradingNode, provider model.InstrumentProvider) {
	riskEngine := risk.New(risk.Limits{}, node.Cache).RequireAccountState()
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
	if err := state.ModeInfo.ValidateVerified(); err != nil {
		t.Fatalf("runtime account %s mode info not verified: %v", accountID, err)
	}
	if !supportsKind(state.ModeInfo.ProductScope, kind) {
		t.Fatalf("runtime account %s scope=%v, want %s", accountID, state.ModeInfo.ProductScope, kind)
	}
	if !acct.IsFresh(time.Now()) {
		t.Fatalf("runtime account %s is not fresh after reconcile: %+v", accountID, acct.Freshness())
	}
	if len(acct.Balances()) == 0 {
		t.Fatalf("runtime account %s has no balances", accountID)
	}
	if _, ok := node.Portfolio.Account(accountID); !ok {
		t.Fatalf("runtime portfolio cannot read account %s", accountID)
	}
	if equity, ok := node.Portfolio.Equity(accountID); !ok || len(equity) == 0 {
		t.Fatalf("runtime portfolio equity=%v ok=%v for account %s, want non-empty", equity, ok, accountID)
	}
	if typ == model.AccountMargin {
		if initial, ok := node.Portfolio.MarginInitial(accountID); !ok || len(initial) == 0 {
			t.Fatalf("runtime portfolio initial margin=%v ok=%v for account %s, want non-empty", initial, ok, accountID)
		}
		if maintenance, ok := node.Portfolio.MarginMaintenance(accountID); !ok || len(maintenance) == 0 {
			t.Fatalf("runtime portfolio maintenance margin=%v ok=%v for account %s, want non-empty", maintenance, ok, accountID)
		}
	}
	metrics := node.Metrics()
	if metrics.Accounts == 0 || metrics.AccountStateAgeNs < 0 {
		t.Fatalf("runtime metrics did not expose account state: %+v", metrics)
	}
	health := node.Health()
	if health.Accounts == 0 || health.AccountStateAgeNs < 0 {
		t.Fatalf("runtime health did not expose account state: %+v", health)
	}
}

func AssertOversizedOrderRejected(t testing.TB, node *btruntime.TradingNode, provider model.InstrumentProvider, id model.InstrumentID) {
	t.Helper()
	inst, ok := provider.Instrument(id)
	if !ok {
		t.Fatalf("instrument provider missing %s", id)
	}
	qty := inst.MinQty
	if !qty.IsPositive() {
		qty = decimal.NewFromInt(1)
	}
	err := risk.New(risk.Limits{}, node.Cache).RequireAccountState().Check(model.OrderRequest{
		ClientID:     "runtime-account-risk-probe",
		InstrumentID: id,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     qty,
		Price:        decimal.NewFromInt(1_000_000_000_000),
		PositionSide: enums.PosNet,
	}, inst)
	if !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("oversized runtime account risk probe err=%v, want ErrRiskRejected", err)
	}
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

func supportsKind(scope []enums.InstrumentKind, kind enums.InstrumentKind) bool {
	if len(scope) == 0 {
		return false
	}
	for _, got := range scope {
		if got == kind {
			return true
		}
	}
	return false
}
