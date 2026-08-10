package httpapi

import "github.com/QuantProcessing/funding-trader/backend/internal/readiness"

type readinessDTO struct {
	Ready           bool                           `json:"ready"`
	Components      map[string]readiness.Component `json:"components"`
	OpenReady       bool                           `json:"openReady"`
	CloseReady      bool                           `json:"closeReady"`
	RecoveryReady   bool                           `json:"recoveryReady"`
	ReadinessReason string                         `json:"readinessReason"`
}

func newReadinessDTO(result readiness.Result, trading readiness.TradingReadinessResult) readinessDTO {
	return readinessDTO{
		Ready:           result.Ready,
		Components:      result.Components,
		OpenReady:       trading.OpenReady,
		CloseReady:      trading.CloseReady,
		RecoveryReady:   trading.RecoveryReady,
		ReadinessReason: string(trading.Reason),
	}
}
