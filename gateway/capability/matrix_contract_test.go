package capability

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestMatrixDescribesImplementedScopeWithoutInventingEvidence(t *testing.T) {
	rows := StaticMatrix().Rows
	if len(rows) != len(routeprofile.DefaultProfiles()) {
		t.Fatal("matrix drifted from route catalog")
	}
	seen := map[string]bool{}
	for _, profile := range routeprofile.DefaultProfiles() {
		row, ok := Resolve(profile.Venue, string(profile.Product), profile.SettlementAsset)
		if !ok || seen[row.RowID] {
			t.Fatalf("missing or duplicate profile: %+v", profile)
		}
		seen[row.RowID] = true
		if row.TradingSupported != (profile.Venue != "lighter" || profile.Product != routeprofile.ProductSpot) {
			t.Fatalf("unexpected trading support: %+v", row)
		}
		if row.Operations.Place != row.TradingSupported || row.Operations.Cancel != row.TradingSupported {
			t.Fatalf("contradictory trading support: %+v", row)
		}
		if row.HistoryPaginationProvesAbsence || row.Operations.Amend || row.Operations.Leverage || row.Operations.MarginMode || row.Operations.PositionMode || row.Operations.FundsTransfer {
			t.Fatalf("unsupported operation: %+v", row)
		}
		if row.ExactLookupStrategy == "" {
			t.Fatal("missing readback strategy")
		}
	}
	for _, parts := range [][3]string{{"unknown", "spot", ""}, {"binance", "perp", "USDC"}, {"binance", "option", ""}, {"binance", "spot", "USDT"}} {
		if _, ok := Resolve(parts[0], parts[1], parts[2]); ok {
			t.Fatalf("invented support: %v", parts)
		}
	}
	raw, err := json.Marshal(StaticMatrix())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"write_ready_default", "exact_fixture", "promotion_evidence", `"passed"`, `"promoted"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("static matrix invents evidence: %s", forbidden)
		}
	}
}
