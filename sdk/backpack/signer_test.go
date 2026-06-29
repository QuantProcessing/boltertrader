package sdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestBuildSigningPayload(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"symbol":    "BTC_USDC",
		"orderType": "Limit",
		"price":     "100",
	}

	got := buildSigningPayload("orderExecute", params, 123456, 5000)
	want := "instruction=orderExecute&orderType=Limit&price=100&symbol=BTC_USDC&timestamp=123456&window=5000"

	if got != want {
		t.Fatalf("buildSigningPayload() = %q, want %q", got, want)
	}
}

func TestBuildSigningPayloadWithoutParamsStillIncludesInstructionAndWindow(t *testing.T) {
	t.Parallel()

	got := buildSigningPayload("balanceQuery", nil, 42, 5000)
	want := "instruction=balanceQuery&timestamp=42&window=5000"

	if got != want {
		t.Fatalf("buildSigningPayload() = %q, want %q", got, want)
	}
}

func TestBuildSigningPayloadOmitsEmptyParams(t *testing.T) {
	t.Parallel()

	got := buildSigningPayload("orderQueryAll", map[string]string{
		"marketType": "PERP",
		"symbol":     "",
	}, 42, 5000)
	want := "instruction=orderQueryAll&marketType=PERP&timestamp=42&window=5000"

	if got != want {
		t.Fatalf("buildSigningPayload() = %q, want %q", got, want)
	}
}

func TestBuildBatchSigningPayloadOrdersEachCommandBeforeWindow(t *testing.T) {
	t.Parallel()

	got := buildBatchSigningPayload("orderExecute", []map[string]string{
		{
			"symbol":    "BTC_USDC",
			"side":      "Bid",
			"orderType": "Limit",
			"price":     "100",
			"quantity":  "1",
		},
		{
			"symbol":    "SOL_USDC",
			"side":      "Ask",
			"orderType": "Market",
			"quantity":  "2",
		},
	}, 1750793021519, 5000)
	want := "instruction=orderExecute&orderType=Limit&price=100&quantity=1&side=Bid&symbol=BTC_USDC&instruction=orderExecute&orderType=Market&quantity=2&side=Ask&symbol=SOL_USDC&timestamp=1750793021519&window=5000"

	if got != want {
		t.Fatalf("buildBatchSigningPayload() = %q, want %q", got, want)
	}
}

func TestSignPayload(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedB64 := base64.StdEncoding.EncodeToString(seed)
	payload := "instruction=balanceQuery&timestamp=42&window=5000"

	got, err := signPayload(seedB64, payload)
	if err != nil {
		t.Fatalf("signPayload() error = %v", err)
	}

	expected := base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(payload)))
	if got != expected {
		t.Fatalf("signPayload() = %q, want %q", got, expected)
	}
}

func TestBuildSignedHeaders(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}

	headers, err := buildSignedHeaders("api-key", base64.StdEncoding.EncodeToString(seed), "balanceQuery", nil, 42, 5000)
	if err != nil {
		t.Fatalf("buildSignedHeaders() error = %v", err)
	}

	if headers["X-API-Key"] != "api-key" {
		t.Fatalf("X-API-Key = %q", headers["X-API-Key"])
	}
	if headers["X-Timestamp"] != "42" {
		t.Fatalf("X-Timestamp = %q", headers["X-Timestamp"])
	}
	if headers["X-Window"] != "5000" {
		t.Fatalf("X-Window = %q", headers["X-Window"])
	}
	if headers["X-Signature"] == "" {
		t.Fatal("X-Signature should not be empty")
	}
}
