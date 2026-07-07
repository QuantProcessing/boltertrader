package runtimeaccept

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
)

func TestRuntimeProductSupportRequiresAccountStateCapabilityForKind(t *testing.T) {
	caps := []contract.Capabilities{
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Trading: true},
			},
			Trading: contract.TradingCapabilities{Submit: true},
		},
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindSpot, Account: true},
			},
			Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
		},
	}

	err := runtimeProductSupportReady(caps, true, enums.KindPerp)
	if err == nil || !strings.Contains(err.Error(), "account-state") {
		t.Fatalf("err=%v, want missing account-state product support", err)
	}
}

func TestRuntimeProductSupportRequiresTradingCapabilityWhenExecutionPresent(t *testing.T) {
	caps := []contract.Capabilities{
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Account: true},
			},
			Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
		},
	}

	err := runtimeProductSupportReady(caps, true, enums.KindPerp)
	if err == nil || !strings.Contains(err.Error(), "trading") {
		t.Fatalf("err=%v, want missing trading product support", err)
	}
}

func TestRuntimeProductSupportReadyAcceptsMatchingTradingAndAccountStateKind(t *testing.T) {
	caps := []contract.Capabilities{
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Trading: true},
			},
			Trading: contract.TradingCapabilities{Submit: true},
		},
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Account: true},
			},
			Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
		},
	}

	if err := runtimeProductSupportReady(caps, true, enums.KindPerp); err != nil {
		t.Fatalf("runtimeProductSupportReady: %v", err)
	}
}
