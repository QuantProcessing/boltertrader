package fundingpolicy

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

var ErrModeDisabled = errors.New("Funding mode is disabled")

type Admission struct {
	liveEnabled      bool
	testMaxNotional  decimal.Decimal
	liveMaxNotional  decimal.Decimal
	riskCheckEnabled bool
}

func DefaultAdmission() Admission {
	return Admission{testMaxNotional: decimal.NewFromInt(100)}
}

func NewAdmission(liveEnabled bool, liveMaxNotional decimal.Decimal) (Admission, error) {
	if liveEnabled && !liveMaxNotional.IsPositive() {
		return Admission{}, fmt.Errorf("enabled Funding Live mode requires a positive maximum notional")
	}
	return Admission{liveEnabled: liveEnabled, testMaxNotional: decimal.NewFromInt(100), liveMaxNotional: liveMaxNotional}, nil
}

func (admission Admission) Allows(mode string, notional decimal.Decimal) bool {
	if admission.testMaxNotional.IsZero() {
		admission = DefaultAdmission()
	}
	if !notional.IsPositive() {
		return false
	}
	switch mode {
	case "test":
		return !notional.GreaterThan(admission.testMaxNotional)
	case "live":
		return admission.liveEnabled && !notional.GreaterThan(admission.liveMaxNotional)
	default:
		return false
	}
}

func (admission Admission) LiveEnabled() bool { return admission.liveEnabled }

// WithRiskCheckEnabled enables the product risk gate. Gateway V1 does not
// currently publish standardized liquidation authority, so calculations fail
// closed while this gate is enabled.
func (admission Admission) WithRiskCheckEnabled(enabled bool) Admission {
	admission.riskCheckEnabled = enabled
	return admission
}

func (admission Admission) RiskCheckEnabled() bool { return admission.riskCheckEnabled }
