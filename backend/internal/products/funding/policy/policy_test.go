package fundingpolicy

import "testing"

func TestRiskCheckPolicyIsExplicitAndDisabledByDefault(t *testing.T) {
	policy := DefaultAdmission()
	if policy.RiskCheckEnabled() {
		t.Fatal("default policy fabricated enabled risk authority")
	}
	if !policy.WithRiskCheckEnabled(true).RiskCheckEnabled() {
		t.Fatal("explicit risk policy enable was not retained")
	}
}
