package contract

import "testing"

func TestAccountStateCapabilitiesAreDistinctFromLegacyBalanceSnapshots(t *testing.T) {
	caps := Capabilities{
		Reports: ReportCapabilities{
			AccountBalanceSnapshots: true,
		},
		Streaming: StreamCapabilities{
			Account: true,
		},
	}
	if caps.Reports.AccountStateSnapshots {
		t.Fatal("legacy balance snapshots should not imply account-state snapshots")
	}
	if caps.Streaming.AccountState {
		t.Fatal("generic account stream should not imply account-state stream")
	}

	caps.Reports.AccountStateSnapshots = true
	caps.Streaming.AccountState = true
	if !caps.Reports.AccountStateSnapshots || !caps.Streaming.AccountState {
		t.Fatal("account-state capability flags should round-trip independently")
	}
}

func TestReferenceDataCapabilitiesAreExplicit(t *testing.T) {
	caps := Capabilities{
		Streaming: StreamCapabilities{Market: true},
	}
	if caps.ReferenceData.CurrentFunding || caps.ReferenceData.CurrentOpenInterest {
		t.Fatal("generic market stream should not imply derivative reference-data support")
	}

	caps.ReferenceData = ReferenceDataCapabilities{
		CurrentFunding:      true,
		CurrentMarkPrice:    true,
		CurrentIndexPrice:   true,
		ReferenceStream:     true,
		CurrentOpenInterest: true,
	}
	if !caps.ReferenceData.CurrentFunding || !caps.ReferenceData.ReferenceStream || !caps.ReferenceData.CurrentOpenInterest {
		t.Fatalf("reference capabilities should round-trip: %+v", caps.ReferenceData)
	}
	if caps.ReferenceData.OpenInterestCached {
		t.Fatal("phase-one OI cache support should remain opt-in and false by default")
	}
}
