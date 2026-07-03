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

func TestAdapterCapabilityMatrixDoesNotClaimAdapterTimestamps(t *testing.T) {
	for _, row := range CapabilityMatrix() {
		if row.LatencyTimestamps {
			t.Fatalf("row %+v claims adapter timestamps before adapter recv/emit fields are populated", row)
		}
	}
}
