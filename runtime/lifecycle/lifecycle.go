package lifecycle

import (
	"fmt"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/model"
)

type NodeState string

const (
	NodeCreated      NodeState = "created"
	NodeStarting     NodeState = "starting"
	NodeReconciling  NodeState = "reconciling"
	NodeRunning      NodeState = "running"
	NodeReconnecting NodeState = "reconnecting"
	NodeStopping     NodeState = "stopping"
	NodeStopped      NodeState = "stopped"
	NodeFailed       NodeState = "failed"
)

type TradingState string

const (
	TradingDisabled    TradingState = "disabled"
	TradingActive      TradingState = "active"
	TradingReconciling TradingState = "reconciling"
	TradingReducing    TradingState = "reducing"
	TradingHalted      TradingState = "halted"
)

var ErrTradingBlocked = fmt.Errorf("trading blocked")

type TransitionError struct {
	From NodeState
	To   NodeState
}

func (e TransitionError) Error() string {
	return fmt.Sprintf("invalid lifecycle transition %s -> %s", e.From, e.To)
}

func ValidTransition(from, to NodeState) bool {
	if from == to {
		return true
	}
	switch from {
	case NodeCreated:
		return to == NodeStarting || to == NodeReconciling || to == NodeReconnecting || to == NodeFailed
	case NodeStarting:
		return to == NodeReconciling || to == NodeRunning || to == NodeFailed || to == NodeStopping
	case NodeReconciling:
		return to == NodeCreated || to == NodeRunning || to == NodeFailed || to == NodeReconnecting || to == NodeStopping
	case NodeRunning:
		return to == NodeReconciling || to == NodeReconnecting || to == NodeStopping || to == NodeFailed
	case NodeReconnecting:
		return to == NodeReconciling || to == NodeFailed || to == NodeStopping
	case NodeStopping:
		return to == NodeStopped || to == NodeFailed
	case NodeStopped:
		return to == NodeStarting
	case NodeFailed:
		return false
	default:
		return false
	}
}

type Snapshot struct {
	Node                    NodeState
	Trading                 TradingState
	Reason                  string
	LastReconciliationError string
}

type Machine struct {
	mu      sync.RWMutex
	node    NodeState
	trading TradingState
	reason  string
	lastErr string
}

func New() *Machine {
	return &Machine{node: NodeCreated, trading: TradingDisabled}
}

func (m *Machine) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Snapshot{Node: m.node, Trading: m.trading, Reason: m.reason, LastReconciliationError: m.lastErr}
}

func (m *Machine) Transition(to NodeState, trading TradingState, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidTransition(m.node, to) {
		return TransitionError{From: m.node, To: to}
	}
	m.node = to
	m.trading = trading
	m.reason = reason
	if to == NodeRunning {
		m.lastErr = ""
	}
	return nil
}

func (m *Machine) ForceFailed(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.node = NodeFailed
	m.trading = TradingHalted
	m.reason = reason
	m.lastErr = reason
}

func (m *Machine) Halt(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trading = TradingHalted
	m.reason = reason
}

func (m *Machine) ReduceOnly(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trading = TradingReducing
	m.reason = reason
}

func (m *Machine) SetLastReconciliationError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.lastErr = ""
		return
	}
	m.lastErr = err.Error()
}

func (m *Machine) CanSubmit(req model.OrderRequest) error {
	s := m.Snapshot()
	switch s.Trading {
	case TradingActive:
		return nil
	case TradingReducing:
		if req.ReduceOnly {
			return nil
		}
		return fmt.Errorf("%w: reduce-only mode blocks new exposure: %s", ErrTradingBlocked, s.Reason)
	default:
		return fmt.Errorf("%w: state=%s trading=%s reason=%s", ErrTradingBlocked, s.Node, s.Trading, s.Reason)
	}
}

func (m *Machine) CanCancel() error {
	s := m.Snapshot()
	switch s.Trading {
	case TradingActive, TradingReconciling, TradingReducing:
		return nil
	default:
		return fmt.Errorf("%w: cancel blocked in state=%s trading=%s reason=%s", ErrTradingBlocked, s.Node, s.Trading, s.Reason)
	}
}

func (m *Machine) CanModify() error {
	s := m.Snapshot()
	if s.Trading == TradingActive {
		return nil
	}
	return fmt.Errorf("%w: modify blocked in state=%s trading=%s reason=%s", ErrTradingBlocked, s.Node, s.Trading, s.Reason)
}
