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

func TestAdapterCapabilityMatrixDocumentsAccountStateSnapshots(t *testing.T) {
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

func TestAdapterCapabilityMatrixDoesNotClaimAdapterTimestamps(t *testing.T) {
	for _, row := range CapabilityMatrix() {
		if row.LatencyTimestamps {
			t.Fatalf("row %+v claims adapter timestamps before adapter recv/emit fields are populated", row)
		}
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
