package capability

import "testing"

func TestUnknownCapabilityRowRequiresPromotionEvidence(t *testing.T) {
	if isPromoted("UNKNOWN") {
		t.Fatal("unknown row bypassed the explicit live-promotion allowlist")
	}
	if got := exactLookupStrategy("UNKNOWN"); got != "requires_fixture_promotion" {
		t.Fatalf("unknown row exact lookup strategy = %q, want evidence gate", got)
	}
}

func TestMatrixHasTwentyRowsNineteenPromotedAndLISReadOnly(t *testing.T) {
	matrix := StaticMatrix()
	if len(matrix.Rows) != 20 {
		t.Fatalf("rows = %d, want 20", len(matrix.Rows))
	}
	seen := make(map[string]bool)
	for _, row := range matrix.Rows {
		if row.RowID == "" || row.Venue == "" || row.Product == "" {
			t.Fatalf("incomplete row: %+v", row)
		}
		if seen[row.RowID] {
			t.Fatalf("duplicate row %q", row.RowID)
		}
		seen[row.RowID] = true
		wantWriteReady := row.Code != "LIS"
		if row.WriteReadyDefault != wantWriteReady {
			t.Fatalf("row %s write-ready=%t, want %t", row.RowID, row.WriteReadyDefault, wantWriteReady)
		}
		if wantWriteReady {
			if row.ExactLookup != "promoted" || row.ExactFixture != "passed" {
				t.Fatalf("promoted row %s has incomplete evidence state: %+v", row.RowID, row)
			}
		} else if row.Code == "LIS" {
			if row.ExactLookup != "fixture_only" || row.ExactFixture != "passed" {
				t.Fatalf("live-blocked row %s lost its fixture-only evidence state: %+v", row.RowID, row)
			}
			if row.PromotionEvidenceRequired != "demo_testnet_two_sided_liquidity_and_exact_lookup" {
				t.Fatalf("live-blocked row %s promotion evidence = %q", row.RowID, row.PromotionEvidenceRequired)
			}
		}
		if row.HistoryPaginationProvesAbsence {
			t.Fatalf("row %s treats pagination as exact reconciliation proof", row.RowID)
		}
		if row.Operations.Leverage {
			t.Fatalf("row %s exposes non-durable leverage mutation in Gateway V1", row.RowID)
		}
		if row.Operations.Amend || row.Operations.MarginMode || row.Operations.PositionMode || row.Operations.FundsTransfer {
			t.Fatalf("row %s claims unsupported operation: %+v", row.RowID, row.Operations)
		}
		if row.ExactLookup == "" {
			t.Fatalf("row %s missing exact lookup declaration", row.RowID)
		}
		if row.Code == "" || row.ExactLookupStrategy == "" || row.ExactFixture == "" || row.PromotionEvidenceRequired == "" {
			t.Fatalf("row %s missing machine exact reconciliation metadata: %+v", row.RowID, row)
		}
		if row.ExactLookupStrategy == "history_pagination" {
			t.Fatalf("row %s uses history pagination as exact strategy", row.RowID)
		}
		switch row.Code {
		case "LIS", "LIP":
			if row.ExactLookupStrategy != "active_plus_first_inactive_page_scan_with_unconfirmed_absence" {
				t.Fatalf("Lighter row %s overstates exact lookup strategy: %q", row.RowID, row.ExactLookupStrategy)
			}
		default:
			if row.ExactLookupStrategy != "client_or_venue_order_id_exact_readback" {
				t.Fatalf("exact-readback row %s strategy = %q", row.RowID, row.ExactLookupStrategy)
			}
		}
	}

	for _, required := range []string{
		"binance:spot", "binance:perp:USDT", "okx:spot", "okx:perp:USDT",
		"lighter:spot", "lighter:perp:USDC", "hyperliquid:spot", "hyperliquid:perp:USDC",
		"bybit:spot", "bybit:perp:USDT", "bybit:perp:USDC", "bitget:spot",
		"bitget:perp:USDT", "bitget:perp:USDC", "gate:spot", "gate:perp:USDT",
		"aster:spot", "aster:perp:USDT", "nado:spot:USDT0", "nado:perp:USDT0",
	} {
		if !seen[required] {
			t.Fatalf("missing row %s", required)
		}
	}

	wantCodes := map[string]bool{"BNS": true, "BNP": true, "OXS": true, "OXP": true, "LIS": true, "LIP": true, "HLS": true, "HLP": true, "BYS": true, "BYU": true, "BYC": true, "BGS": true, "BGU": true, "BGC": true, "GTS": true, "GTU": true, "ATS": true, "ATP": true, "NDS": true, "NDP": true}
	for _, row := range matrix.Rows {
		if !wantCodes[row.Code] {
			t.Fatalf("row %s has non-manifest code %q", row.RowID, row.Code)
		}
		delete(wantCodes, row.Code)
	}
	if len(wantCodes) != 0 {
		t.Fatalf("missing manifest row codes: %v", wantCodes)
	}
}
