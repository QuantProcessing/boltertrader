package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTypedResourceIDsValidateShape(t *testing.T) {
	validUUIDv7 := "018f0000-0000-7000-8000-000000000001"
	validUUIDv7Variants := []string{
		"018f0000-0000-7000-8000-000000000001",
		"018f0000-0000-7000-9000-000000000001",
		"018f0000-0000-7000-a000-000000000001",
		"018F0000-0000-7000-B000-000000000001",
	}
	invalidUUIDs := []string{
		"018f0000-0000-4000-8000-000000000001", // wrong version
		"018f0000-0000-7000-7000-000000000001", // non-RFC variant
		"018f0000-0000-7000-c000-000000000001", // reserved variant
		"018f0000-0000-7000-8000-00000000000g", // non-hex payload
		"018f000z-0000-7000-8000-000000000001", // non-hex group
	}
	for name, run := range map[string]func(string) error{
		"principal":     func(v string) error { return PrincipalID(v).Validate() },
		"account":       func(v string) error { return AccountID(v).Validate() },
		"idempotency":   func(v string) error { return IdempotencyKey(v).Validate() },
		"command":       func(v string) error { return CommandID(v).Validate() },
		"gateway_order": func(v string) error { return GatewayOrderID(v).Validate() },
		"event":         func(v string) error { return EventID(v).Validate() },
		"instrument_id": func(v string) error { return InstrumentID(v).Validate() },
		"resource_version": func(string) error {
			return ResourceVersion(0).Validate()
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := "principal-a"
			if strings.Contains(name, "command") || strings.Contains(name, "order") || name == "event" {
				value = validUUIDv7
			}
			if name == "instrument_id" {
				value = "binance:live:spot:BTC-USDT"
			}
			if err := run(value); err != nil {
				t.Fatalf("valid %s rejected: %v", name, err)
			}
			if name == "command" || name == "gateway_order" || name == "event" {
				for _, valid := range validUUIDv7Variants {
					if err := run(valid); err != nil {
						t.Fatalf("%s rejected valid uuidv7 variant %q: %v", name, valid, err)
					}
				}
				for _, invalid := range invalidUUIDs {
					if err := run(invalid); err == nil {
						t.Fatalf("%s accepted invalid uuidv7 %q", name, invalid)
					}
				}
			}
		})
	}
	if err := ResourceVersion(-1).Validate(); err == nil {
		t.Fatal("negative resource version accepted")
	}
}

func TestStableInstrumentIDIsGatewayOwnedAndDoesNotUseNativeSymbols(t *testing.T) {
	id, err := NewInstrumentID("binance", "live", "perp", "USDT", "BTC-USDT")
	if err != nil {
		t.Fatalf("NewInstrumentID: %v", err)
	}
	if got, want := id.String(), "binance:live:perp:USDT:BTC-USDT"; got != want {
		t.Fatalf("instrument id = %q, want %q", got, want)
	}
	if _, err := NewInstrumentID("binance", "live", "perp", "BTCUSDT", "BTCUSDT"); err == nil {
		t.Fatal("native-style symbol/settlement accepted as stable gateway identity")
	}
}

func TestStableInstrumentIDEscapesVenueSymbolNamespaces(t *testing.T) {
	id, err := NewInstrumentID("hyperliquid", "live", "perp", "USDC", "xyz:TSLA-USDC")
	if err != nil {
		t.Fatalf("NewInstrumentID: %v", err)
	}
	if got, want := id.String(), "hyperliquid:live:perp:USDC:xyz%3ATSLA-USDC"; got != want {
		t.Fatalf("instrument id = %q, want %q", got, want)
	}
	if got, want := id.Product(), "perp"; got != want {
		t.Fatalf("product = %q, want %q", got, want)
	}
	if got, want := id.Settlement(), "USDC"; got != want {
		t.Fatalf("settlement = %q, want %q", got, want)
	}

	percent, err := NewInstrumentID("hyperliquid", "live", "perp", "USDC", "xyz%3ATSLA-USDC")
	if err != nil {
		t.Fatalf("NewInstrumentID percent: %v", err)
	}
	if percent == id {
		t.Fatal("percent-bearing symbol collided with a namespaced symbol")
	}
}

func TestClientOrderIDRequiresPositiveUint48Decimal(t *testing.T) {
	valid := []string{"1", "42", "281474976710655"}
	for _, value := range valid {
		if id, err := ParseClientOrderID(value); err != nil || id.String() != value {
			t.Fatalf("ParseClientOrderID(%q) = %q, %v", value, id.String(), err)
		}
	}

	invalid := []string{"", "0", "-1", "01", "281474976710656", "1.0", "abc"}
	for _, value := range invalid {
		if _, err := ParseClientOrderID(value); err == nil {
			t.Fatalf("ParseClientOrderID(%q) succeeded", value)
		}
	}
}

func TestCanonicalPayloadHashIsStableAndSemantic(t *testing.T) {
	left := json.RawMessage(`{"instrument_id":"binance:live:spot:BTC-USDT","account_id":"binance-main","quantity":"0.001"}`)
	right := json.RawMessage(`{"quantity":"0.001","account_id":"binance-main","instrument_id":"binance:live:spot:BTC-USDT"}`)
	changed := json.RawMessage(`{"quantity":"0.002","account_id":"binance-main","instrument_id":"binance:live:spot:BTC-USDT"}`)

	hashLeft, err := CanonicalPayloadHash(left)
	if err != nil {
		t.Fatalf("CanonicalPayloadHash(left): %v", err)
	}
	hashRight, err := CanonicalPayloadHash(right)
	if err != nil {
		t.Fatalf("CanonicalPayloadHash(right): %v", err)
	}
	if hashLeft != hashRight {
		t.Fatalf("hash changed with JSON field order: %s vs %s", hashLeft, hashRight)
	}
	hashChanged, err := CanonicalPayloadHash(changed)
	if err != nil {
		t.Fatalf("CanonicalPayloadHash(changed): %v", err)
	}
	if hashLeft == hashChanged {
		t.Fatal("hash ignored semantic payload change")
	}
	if _, err := CanonicalPayloadHash(json.RawMessage(`{"quantity":}`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	if _, err := CanonicalPayloadHash(json.RawMessage(`{"quantity":"1"} {"extra":true}`)); err == nil {
		t.Fatal("multiple top-level JSON values accepted")
	}
}

func TestOrderStateTransitionsAreFrozen(t *testing.T) {
	legal := []struct {
		from OrderState
		to   OrderState
	}{
		{"prepared", "dispatching"},
		{"prepared", "rejected"},
		{"dispatching", "accepted"},
		{"dispatching", "uncertain"},
		{"accepted", "open"},
		{"accepted", "partially_filled"},
		{"accepted", "filled"},
		{"accepted", "canceled"},
		{"accepted", "rejected"},
		{"accepted", "uncertain"},
		{"open", "partially_filled"},
		{"open", "filled"},
		{"partially_filled", "filled"},
		{"open", "canceled"},
		{"open", "uncertain"},
		{"partially_filled", "canceled"},
		{"partially_filled", "uncertain"},
		{"dispatching", "rejected"},
		{"uncertain", "accepted"},
		{"uncertain", "open"},
		{"uncertain", "partially_filled"},
		{"uncertain", "filled"},
		{"uncertain", "canceled"},
		{"uncertain", "rejected"},
	}
	for _, edge := range legal {
		if err := ValidateOrderTransition(edge.from, edge.to); err != nil {
			t.Fatalf("%s -> %s rejected: %v", edge.from, edge.to, err)
		}
	}

	illegal := []struct {
		from OrderState
		to   OrderState
	}{
		{"filled", "open"},
		{"canceled", "dispatching"},
		{"rejected", "prepared"},
		{"prepared", "filled"},
	}
	for _, edge := range illegal {
		if err := ValidateOrderTransition(edge.from, edge.to); err == nil {
			t.Fatalf("%s -> %s accepted", edge.from, edge.to)
		}
	}
}
