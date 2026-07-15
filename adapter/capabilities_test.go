package adapter

import (
	"os"
	"strings"
	"testing"
)

func TestAdapterCapabilityMatrixRowsAreDocumented(t *testing.T) {
	data, err := os.ReadFile("../docs/adapter-capabilities.md")
	if err != nil {
		t.Fatalf("read docs matrix: %v", err)
	}
	doc := string(data)
	for _, row := range CapabilityMatrix() {
		for _, want := range []string{row.Venue, row.Product, row.DemoTarget} {
			if !strings.Contains(doc, want) {
				t.Fatalf("docs/adapter-capabilities.md missing %q for row %+v", want, row)
			}
		}
	}
}

func TestAdapterCapabilityMatrixRowsAreUniqueAndTargetsExist(t *testing.T) {
	data, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(data)
	seen := make(map[string]struct{})
	for _, row := range CapabilityMatrix() {
		key := row.Venue + "|" + row.Product
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate capability row %s", key)
		}
		seen[key] = struct{}{}
		target := strings.TrimPrefix(row.DemoTarget, "make ")
		if target == row.DemoTarget || target == "" {
			t.Fatalf("row %s has invalid acceptance target %q", key, row.DemoTarget)
		}
		if !strings.Contains(makefile, "\n"+target+":") {
			t.Fatalf("row %s references missing Make target %q", key, target)
		}
	}
}

func TestAdapterCapabilityMatrixDocumentsMandatoryAccountState(t *testing.T) {
	data, err := os.ReadFile("../docs/adapter-capabilities.md")
	if err != nil {
		t.Fatalf("read docs matrix: %v", err)
	}
	if !strings.Contains(string(data), "Account-state snapshot") {
		t.Fatal("docs/adapter-capabilities.md must expose the account-state snapshot capability column")
	}
	want := map[string]bool{
		"ASTER|Spot cash":              true,
		"ASTER|USDT-linear Perp":       true,
		"NADO|Spot no-borrow":          true,
		"NADO|Perp":                    true,
		"BINANCE|USD-M Perp":           true,
		"BINANCE|Spot":                 true,
		"OKX|USDT-linear SWAP":         true,
		"OKX|Spot cash":                true,
		"BYBIT|Spot cash":              true,
		"BYBIT|USDT-linear Perp/SWAP":  true,
		"BYBIT|USDC-linear Perp/SWAP":  true,
		"BITGET|Spot cash":             true,
		"BITGET|USDT-linear Perp/SWAP": true,
		"BITGET|USDC-linear Perp/SWAP": true,
		"GATE|Spot cash":               true,
		"GATE|USDT-linear Perp/SWAP":   true,
		"HYPERLIQUID|Spot cash":        true,
		"HYPERLIQUID|Perp":             true,
		"HYPERLIQUID|HIP-3 Perp":       true,
		"LIGHTER|Spot cash":            true,
		"LIGHTER|Perp":                 true,
	}
	for _, row := range CapabilityMatrix() {
		key := row.Venue + "|" + row.Product
		if got := row.AccountStateSnapshot; got != want[key] {
			t.Fatalf("%s AccountStateSnapshot=%v, want %v", key, got, want[key])
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("capability matrix missing expected rows: %v", want)
	}
}

func TestAdapterCapabilityMatrixDoesNotClaimLatencyTimestamps(t *testing.T) {
	for _, row := range CapabilityMatrix() {
		if row.LatencyTimestamps {
			t.Fatalf("row %+v claims latency timestamps before recv/emit fields are populated", row)
		}
	}
}

func TestAdapterCapabilityMatrixMatchesImplementedUnifiedFillRecovery(t *testing.T) {
	docBytes, err := os.ReadFile("../docs/adapter-capabilities.md")
	if err != nil {
		t.Fatalf("read docs matrix: %v", err)
	}
	doc := string(docBytes)
	wantRows := map[string]struct{}{
		"BYBIT|Spot cash":              {},
		"BYBIT|USDT-linear Perp/SWAP":  {},
		"BYBIT|USDC-linear Perp/SWAP":  {},
		"BITGET|Spot cash":             {},
		"BITGET|USDT-linear Perp/SWAP": {},
		"BITGET|USDC-linear Perp/SWAP": {},
	}
	for _, row := range CapabilityMatrix() {
		key := row.Venue + "|" + row.Product
		if _, ok := wantRows[key]; !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row.FillReports), "unsupported") {
			t.Fatalf("%s reports implemented fill history as unsupported", key)
		}
		if !strings.Contains(strings.ToLower(row.MassStatus), "fill") {
			t.Fatalf("%s mass-status description omits implemented fills: %q", key, row.MassStatus)
		}
		documented := "| " + row.Venue + " | " + row.Product + " |"
		claims := "| " + row.FillReports + " | " + row.PositionReports + " | " + row.MassStatus + " |"
		matched := false
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, documented) && strings.Contains(line, claims) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("docs row for %s does not match fill/position/mass-status claims %q", key, claims)
		}
		delete(wantRows, key)
	}
	if len(wantRows) != 0 {
		t.Fatalf("capability matrix is missing unified fill rows: %v", wantRows)
	}
}

func TestAdapterCapabilityMatrixMassStatusListsEveryDirectRecoveryDomain(t *testing.T) {
	docBytes, err := os.ReadFile("../docs/adapter-capabilities.md")
	if err != nil {
		t.Fatalf("read docs matrix: %v", err)
	}
	doc := string(docBytes)
	want := map[string]string{
		"ASTER|Spot cash":            "open orders, bounded fills",
		"ASTER|USDT-linear Perp":     "open orders, bounded fills, positions",
		"NADO|Spot no-borrow":        "open orders, bounded fills",
		"NADO|Perp":                  "open orders, bounded fills, positions",
		"GATE|Spot cash":             "open orders, bounded fills",
		"GATE|USDT-linear Perp/SWAP": "open orders, bounded fills, positions",
		"LIGHTER|Perp":               "open orders, positions",
	}
	for _, row := range CapabilityMatrix() {
		key := row.Venue + "|" + row.Product
		massStatus, ok := want[key]
		if !ok {
			continue
		}
		if row.MassStatus != massStatus {
			t.Fatalf("%s mass status=%q, want direct domains %q", key, row.MassStatus, massStatus)
		}
		prefix := "| " + row.Venue + " | " + row.Product + " |"
		claims := "| " + row.FillReports + " | " + row.PositionReports + " | " + row.MassStatus + " |"
		matched := false
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, prefix) && strings.Contains(line, claims) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("docs row for %s does not match direct recovery-domain claims %q", key, claims)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("capability matrix is missing direct recovery rows: %v", want)
	}
}

func TestAdapterCapabilityMatrixDoesNotClaimNadoSpotPositionReports(t *testing.T) {
	for _, row := range CapabilityMatrix() {
		if row.Venue == "NADO" && row.Product == "Spot no-borrow" {
			if row.PositionReports != "unsupported" {
				t.Fatalf("Nado Spot position reports=%q, want unsupported", row.PositionReports)
			}
			return
		}
	}
	t.Fatal("Nado Spot capability row is missing")
}

func TestAdapterCapabilityMatrixDoesNotClaimBinanceSpotModify(t *testing.T) {
	found := false
	for _, row := range CapabilityMatrix() {
		if row.Venue != "BINANCE" || row.Product != "Spot" {
			continue
		}
		found = true
		if row.Modify {
			t.Fatal("Binance Spot capability inventory claims Modify even though the adapter returns ErrNotSupported")
		}
	}
	if !found {
		t.Fatal("Binance Spot capability row is missing")
	}

	data, err := os.ReadFile("../docs/adapter-capabilities.md")
	if err != nil {
		t.Fatalf("read docs matrix: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "| BINANCE | Spot |") {
			continue
		}
		columns := strings.Split(strings.Trim(line, "|"), "|")
		if len(columns) < 9 {
			t.Fatalf("Binance Spot documentation row has %d columns, want at least 9: %q", len(columns), line)
		}
		if got := strings.TrimSpace(columns[8]); got != "no" {
			t.Fatalf("Binance Spot documented Modify=%q, want no", got)
		}
		return
	}
	t.Fatal("docs/adapter-capabilities.md is missing the Binance Spot row")
}
