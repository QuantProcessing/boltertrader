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
