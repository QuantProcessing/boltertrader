package factoryclient

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestFundingPaymentCursorIsOpaqueSignedAndScopeBound(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	instrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{NativeSymbol: "BTCUSDT", NativeID: "native-BTC"}}
	scope := fundingPaymentCursorScope(&instrument, "USDT", start, end, 100)
	key := fundingPaymentCursorKey("venue=binance", "account=one", "secret=alpha")

	if _, err := decodeFundingPaymentCursor("2", key, scope); err == nil {
		t.Fatal("raw page cursor was accepted")
	}
	cursor := encodeFundingPaymentCursor(key, fundingPaymentCursorEnvelope{ScopeDigest: scope.ScopeDigest, Page: 2, ItemsSeen: 100})
	if strings.Contains(cursor, "BTCUSDT") || strings.Contains(cursor, "native-BTC") {
		t.Fatalf("cursor exposed native identifier: %s", cursor)
	}
	if decoded := decodeCursorTokenForTest(t, cursor); strings.Contains(decoded, "BTCUSDT") || strings.Contains(decoded, "native-BTC") {
		t.Fatalf("decoded cursor payload exposed native identifier: %s", decoded)
	}
	if _, err := decodeFundingPaymentCursor(cursor, key, scope); err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	mismatch := fundingPaymentCursorScope(&instrument, "USDC", start, end, 100)
	if _, err := decodeFundingPaymentCursor(cursor, key, mismatch); err == nil {
		t.Fatal("scope-mismatched cursor was accepted")
	}
	if _, err := decodeFundingPaymentCursor(cursor, fundingPaymentCursorKey("venue=binance", "account=two", "secret=alpha"), scope); err == nil {
		t.Fatal("cross-account cursor was accepted")
	}
	if _, err := decodeFundingPaymentCursor(cursor, fundingPaymentCursorKey("venue=binance", "account=one", "secret=beta"), scope); err == nil {
		t.Fatal("cross-credential cursor was accepted")
	}
}

func TestFundingPaymentCompletionUsesCumulativeTraversalItems(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	asOf := end.Add(time.Second)
	if got := fundingPaymentCompletionStatus(0, false, start, end, true, asOf); got != exchange.FundingPaymentsCompleteEmpty {
		t.Fatalf("empty initial traversal status=%s", got)
	}
	if got := fundingPaymentCompletionStatus(1, false, start, end, true, asOf); got != exchange.FundingPaymentsComplete {
		t.Fatalf("nonempty traversal with empty terminal page status=%s", got)
	}
	if got := fundingPaymentCompletionStatus(1, false, start, end, false, asOf); got != exchange.FundingPaymentsIncomplete {
		t.Fatalf("non-retained traversal status=%s", got)
	}
	if got := fundingPaymentCompletionStatus(1, false, start, end, true, end.Add(-time.Nanosecond)); got != exchange.FundingPaymentsIncomplete {
		t.Fatalf("future-ended traversal status=%s", got)
	}
	watermark := fundingPaymentWatermark("", end, exchange.FundingPaymentsComplete, end.Add(-time.Nanosecond))
	if watermark.Verified || !watermark.CompleteThrough.IsZero() || !watermark.EffectiveThrough.IsZero() {
		t.Fatalf("future-ended watermark verified completeness: %+v", watermark)
	}
}

func TestFundingPaymentCursorRejectsPayloadAndSignatureTampering(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	scope := fundingPaymentCursorScope(nil, "USDT", start, end, 100)
	key := fundingPaymentCursorKey("venue=lighter", "account=7", "secret=alpha")
	cursor := encodeFundingPaymentCursor(key, fundingPaymentCursorEnvelope{
		ScopeDigest: scope.ScopeDigest, VenueCursor: "native-next", ItemsSeen: 10,
	})
	if _, err := decodeFundingPaymentCursor(tamperCursorPayloadForTest(t, cursor, func(envelope *fundingPaymentCursorEnvelope) {
		envelope.ItemsSeen = 999
	}), key, scope); err == nil {
		t.Fatal("items_seen-tampered cursor was accepted")
	}
	if _, err := decodeFundingPaymentCursor(tamperCursorPayloadForTest(t, cursor, func(envelope *fundingPaymentCursorEnvelope) {
		envelope.VenueCursor = "other-native-next"
	}), key, scope); err == nil {
		t.Fatal("venue-cursor-tampered cursor was accepted")
	}
	if _, err := decodeFundingPaymentCursor(tamperCursorPayloadForTest(t, cursor, func(envelope *fundingPaymentCursorEnvelope) {
		envelope.Page = 7
	}), key, scope); err == nil {
		t.Fatal("page-tampered cursor was accepted")
	}
	if _, err := decodeFundingPaymentCursor(tamperCursorPayloadForTest(t, cursor, func(envelope *fundingPaymentCursorEnvelope) {
		envelope.SliceStartMillis = start.Add(7 * 24 * time.Hour).UnixMilli()
	}), key, scope); err == nil {
		t.Fatal("slice-start-tampered cursor was accepted")
	}
	if _, err := decodeFundingPaymentCursor(tamperCursorSignatureForTest(t, cursor), key, scope); err == nil {
		t.Fatal("signature-tampered cursor was accepted")
	}
}

func TestFundingPaymentRetainedSinceUsesThreeCalendarMonths(t *testing.T) {
	cases := []struct {
		name  string
		asOf  time.Time
		start time.Time
		want  bool
	}{
		{"month-end cutoff inclusive", time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC), true},
		{"before month-end cutoff incomplete", time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC), time.Date(2026, 2, 27, 23, 59, 59, 0, time.UTC), false},
		{"leap day cutoff inclusive", time.Date(2024, 5, 31, 12, 0, 0, 0, time.UTC), time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC), true},
		{"before leap day cutoff incomplete", time.Date(2024, 5, 31, 12, 0, 0, 0, time.UTC), time.Date(2024, 2, 28, 23, 59, 59, 0, time.UTC), false},
		{"short month exact cutoff", time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fundingPaymentRetainedSince(tc.start, tc.asOf, 3); got != tc.want {
				t.Fatalf("retained=%t want %t", got, tc.want)
			}
		})
	}
}

func decodeCursorTokenForTest(t *testing.T, cursor string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "fp:v1:"))
	if err != nil {
		t.Fatal(err)
	}
	var token fundingPaymentCursorToken
	if err := json.Unmarshal(raw, &token); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(token.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + string(payload)
}

func tamperCursorPayloadForTest(t *testing.T, cursor string, mutate func(*fundingPaymentCursorEnvelope)) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "fp:v1:"))
	if err != nil {
		t.Fatal(err)
	}
	var token fundingPaymentCursorToken
	if err := json.Unmarshal(raw, &token); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(token.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope fundingPaymentCursorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	mutate(&envelope)
	payload, _ = json.Marshal(envelope)
	token.Payload = base64.RawURLEncoding.EncodeToString(payload)
	raw, _ = json.Marshal(token)
	return "fp:v1:" + base64.RawURLEncoding.EncodeToString(raw)
}

func tamperCursorSignatureForTest(t *testing.T, cursor string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "fp:v1:"))
	if err != nil {
		t.Fatal(err)
	}
	var token fundingPaymentCursorToken
	if err := json.Unmarshal(raw, &token); err != nil {
		t.Fatal(err)
	}
	token.Signature = strings.Repeat("A", len(token.Signature))
	raw, _ = json.Marshal(token)
	return "fp:v1:" + base64.RawURLEncoding.EncodeToString(raw)
}
