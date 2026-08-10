package httpapi

import (
	"errors"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
)

type verificationStatusDTO struct {
	Status        string             `json:"status"`
	Reason        string             `json:"reason,omitempty"`
	Details       string             `json:"details,omitempty"`
	Watermark     ledgerWatermarkDTO `json:"watermark"`
	OpenReady     bool               `json:"openReady"`
	CloseReady    bool               `json:"closeReady"`
	RecoveryReady bool               `json:"recoveryReady"`
}

func newVerificationStatusDTO(
	status string,
	reason string,
	details string,
	watermark ledgerprojection.Watermark,
	trading readiness.TradingReadinessResult,
) (verificationStatusDTO, error) {
	status = normalizeLedgerVerificationStatus(status)
	if !validLedgerVerificationStatus(status) {
		return verificationStatusDTO{}, errors.New("invalid verification status")
	}
	return verificationStatusDTO{
		Status:        status,
		Reason:        reason,
		Details:       details,
		Watermark:     newLedgerWatermarkDTO(watermark),
		OpenReady:     trading.OpenReady,
		CloseReady:    trading.CloseReady,
		RecoveryReady: trading.RecoveryReady,
	}, nil
}
