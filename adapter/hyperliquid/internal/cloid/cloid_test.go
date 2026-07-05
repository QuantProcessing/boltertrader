package cloid

import "testing"

func TestForClientIDReturnsStable128BitHexCloid(t *testing.T) {
	got := ForClientID("bt-hl-runtime-123")
	if len(got) != 34 {
		t.Fatalf("cloid len=%d, want 34 for 0x + 32 hex chars: %q", len(got), got)
	}
	if got[:2] != "0x" {
		t.Fatalf("cloid=%q, want 0x prefix", got)
	}
	for _, r := range got[2:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("cloid=%q contains non-hex rune %q", got, r)
		}
	}
	if again := ForClientID("bt-hl-runtime-123"); again != got {
		t.Fatalf("cloid not stable: got %q then %q", got, again)
	}
}

func TestForClientIDPreservesAlreadyValidCloid(t *testing.T) {
	raw := "0x1234567890abcdef1234567890abcdef"
	if got := ForClientID(raw); got != raw {
		t.Fatalf("ForClientID(%q)=%q, want unchanged", raw, got)
	}
}

func TestMapperRestoresOriginalClientIDFromVenueCloidAndOrderID(t *testing.T) {
	mapper := NewMapper()
	original := "hyperliquid-runtime-1700000000000-1"
	venueCloid := mapper.VenueCloid(original)
	mapper.Remember(original, venueCloid, "555")

	if got := mapper.ClientID(venueCloid, ""); got != original {
		t.Fatalf("ClientID(mapped cloid)=%q, want %q", got, original)
	}
	if got := mapper.ClientID("", "555"); got != original {
		t.Fatalf("ClientID(venue order id)=%q, want %q", got, original)
	}
	if got := mapper.ClientID("0x00000000000000000000000000000000", "999"); got != "0x00000000000000000000000000000000" {
		t.Fatalf("unknown mapping=%q, want venue cloid passthrough", got)
	}
}
