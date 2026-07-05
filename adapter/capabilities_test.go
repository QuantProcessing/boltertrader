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

func TestAdapterCapabilityMatrixDocumentsAccountStateSnapshots(t *testing.T) {
	data, err := os.ReadFile("../docs/adapter-capabilities.md")
	if err != nil {
		t.Fatalf("read docs matrix: %v", err)
	}
	if !strings.Contains(string(data), "Account-state snapshot") {
		t.Fatal("docs/adapter-capabilities.md must expose the account-state snapshot capability column")
	}
	want := map[string]bool{
		"BINANCE|USD-M Perp":     true,
		"BINANCE|Spot":           true,
		"OKX|USDT-linear SWAP":   true,
		"OKX|Spot cash":          true,
		"HYPERLIQUID|Spot cash":  false,
		"HYPERLIQUID|Perp":       false,
		"HYPERLIQUID|HIP-3 Perp": false,
		"LIGHTER|Spot cash":      true,
		"LIGHTER|Perp":           true,
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
