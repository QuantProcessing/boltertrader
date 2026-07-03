package lifecycle

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestLifecycleTransitions(t *testing.T) {
	m := New()
	for _, step := range []struct {
		node    NodeState
		trading TradingState
	}{
		{NodeStarting, TradingDisabled},
		{NodeReconciling, TradingReconciling},
		{NodeRunning, TradingActive},
		{NodeStopping, TradingDisabled},
		{NodeStopped, TradingDisabled},
	} {
		if err := m.Transition(step.node, step.trading, "test"); err != nil {
			t.Fatalf("transition to %s: %v", step.node, err)
		}
	}
}

func TestPreRunReconciliationCanReturnToCreated(t *testing.T) {
	m := New()
	if err := m.Transition(NodeReconciling, TradingReconciling, "pre-run resync"); err != nil {
		t.Fatalf("transition to reconciling: %v", err)
	}
	if err := m.Transition(NodeCreated, TradingDisabled, "pre-run resync complete"); err != nil {
		t.Fatalf("transition back to created: %v", err)
	}
	if err := m.Transition(NodeStarting, TradingDisabled, "run start"); err != nil {
		t.Fatalf("created should remain startable: %v", err)
	}
}

func TestInvalidTransition(t *testing.T) {
	m := New()
	err := m.Transition(NodeRunning, TradingActive, "skip")
	var transErr TransitionError
	if !errors.As(err, &transErr) {
		t.Fatalf("err=%v, want TransitionError", err)
	}
}

func TestHaltedCannotReturnToRunningWithoutReset(t *testing.T) {
	m := New()
	m.ForceFailed("startup failed")
	if err := m.Transition(NodeRunning, TradingActive, "resume"); err == nil {
		t.Fatal("failed state should not transition directly to running")
	}
}

func TestReducingAllowsOnlyReduceCancel(t *testing.T) {
	m := New()
	if err := m.Transition(NodeStarting, TradingDisabled, "start"); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(NodeRunning, TradingActive, "run"); err != nil {
		t.Fatal(err)
	}
	m.ReduceOnly("operator")
	req := model.OrderRequest{Side: enums.SideBuy}
	if err := m.CanSubmit(req); !errors.Is(err, ErrTradingBlocked) {
		t.Fatalf("new exposure err=%v, want blocked", err)
	}
	req.ReduceOnly = true
	if err := m.CanSubmit(req); err != nil {
		t.Fatalf("reduce-only submit should pass: %v", err)
	}
	if err := m.CanCancel(); err != nil {
		t.Fatalf("cancel should pass in reducing: %v", err)
	}
}

func TestStateSnapshotImmutable(t *testing.T) {
	m := New()
	s := m.Snapshot()
	s.Reason = "mutated"
	if got := m.Snapshot().Reason; got == "mutated" {
		t.Fatal("snapshot mutation affected machine")
	}
}
