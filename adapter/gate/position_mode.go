package gate

import (
	"fmt"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

type futuresPositionModeState struct {
	mu   sync.RWMutex
	mode string
}

func newFuturesPositionModeState() *futuresPositionModeState {
	return &futuresPositionModeState{}
}

func (s *futuresPositionModeState) setAccount(account *gatesdk.FuturesAccount) error {
	if s == nil || account == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(account.PositionMode))
	if mode == "" {
		if account.InDualMode {
			mode = "dual"
		} else {
			mode = "single"
		}
	}
	switch mode {
	case "single", "dual":
	case "split":
		return fmt.Errorf("gate: futures split position mode is not supported")
	default:
		return fmt.Errorf("gate: unknown futures position mode %q", account.PositionMode)
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	return nil
}

func (s *futuresPositionModeState) current() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *futuresPositionModeState) orderPositionSide(order gatesdk.FuturesOrder) (enums.PositionSide, bool) {
	return positionSideFromGateOrder(order, s.current())
}

func positionSideFromGateOrder(order gatesdk.FuturesOrder, mode string) (enums.PositionSide, bool) {
	if strings.EqualFold(strings.TrimSpace(mode), "single") {
		return enums.PosNet, true
	}
	if strings.EqualFold(strings.TrimSpace(mode), "dual") {
		if order.Size == 0 {
			switch strings.ToLower(strings.TrimSpace(order.AutoSize)) {
			case "close_long":
				return enums.PosLong, true
			case "close_short":
				return enums.PosShort, true
			default:
				return enums.PosNet, false
			}
		}
		if order.ReduceOnly || order.IsReduceOnly {
			if order.Size > 0 {
				return enums.PosShort, true
			}
			return enums.PosLong, true
		}
		return positionSideFromGate(order.Size), true
	}
	return enums.PosNet, false
}
