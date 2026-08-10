package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/shopspring/decimal"
)

func TestFundingPaymentPageDTOUsesContractEncoding(t *testing.T) {
	start := time.Date(2026, 7, 29, 1, 2, 3, 4, time.FixedZone("source", 8*60*60))
	end := start.Add(8 * time.Hour)
	observed := start.Add(2 * time.Minute)
	effective := start.Add(time.Minute)
	completeThrough := end.Add(-time.Nanosecond)
	scope := ledgerfacts.Scope{
		PrincipalID:         "principal-1",
		Mode:                "test",
		AccountID:           "account-1",
		InstrumentID:        "binance:testnet:perp:USDT:BTC-USDT",
		WindowStart:         start,
		WindowEnd:           end,
		PaginationTraversal: "cursor:page-1",
	}

	dto := newFundingPaymentPageDTO(fundingPaymentPage{
		Items: []ledgerfacts.FundingPaymentFact{{
			FactID:           "fact-1",
			SupersedesFactID: "fact-0",
			PaymentID:        "payment-1",
			Venue:            "binance",
			VenueEnvironment: "testnet",
			Product:          "perp",
			FormulaVersion:   "ledger-v1",
			Scope:            scope,
			Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1.23000001")},
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "opaque-source-ref",
				SourceRevision: "17",
				SourceVersion:  "gateway-v1",
				ObservedAt:     observed,
				EffectiveAt:    effective,
			},
		}},
		NextCursor: "next-cursor",
		Scope:      scope,
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			Cursor:           "cursor-1",
			Revision:         "17",
			ObservedAt:       observed,
			EffectiveThrough: effective,
			CompleteThrough:  completeThrough,
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
		Status:          ledgerfacts.Complete,
		AsOf:            observed,
		CompleteThrough: completeThrough,
		Revision:        "17",
	})

	body := mustJSON(t, dto)
	assertJSONValue(t, body, "items.0.amount", "-1.23000001")
	assertJSONValue(t, body, "items.0.effectiveAt", "2026-07-28T17:03:03.000000004Z")
	assertJSONValue(t, body, "items.0.observedAt", "2026-07-28T17:04:03.000000004Z")
	assertJSONValue(t, body, "watermark.completeThrough", "2026-07-29T01:02:03.000000003Z")
	assertJSONValue(t, body, "scope.instrumentId", "binance:testnet:perp:USDT:BTC-USDT")
	assertJSONValue(t, body, "status", "complete")
	assertJSONValue(t, body, "revision", "17")
	assertJSONOmits(t, body, "secret", "apiKey", "credential", "raw")
}

func TestClosedPositionLedgerDTOUsesProjectionEconomicsAndReadmodelMetadata(t *testing.T) {
	asOf := time.Date(2026, 7, 29, 4, 5, 6, 7, time.FixedZone("source", -5*60*60))
	openedAt := asOf.Add(-2 * time.Hour)
	closedAt := asOf.Add(-time.Minute)
	conversionAt := asOf.Add(-time.Second)
	projection := ledgerprojection.ClosedPositionProjection{
		PositionID:            "position-1",
		Scope:                 ledgerfacts.Scope{PrincipalID: "principal-1", Mode: "test", AccountID: "account-ledger", InstrumentID: "instrument-ledger"},
		ReportingAsset:        "USDT",
		AsOf:                  asOf,
		RealizedTradingPnl:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("10.50")},
		UnrealizedTradingPnl:  ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		ActualFundingCashFlow: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.75")},
		Fees:                  ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.05")},
		NetPnl:                ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("9.70")},
		ClosedTradingUPnL:     ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		Verified:              true,
		VerificationStatus:    "verified",
		Residual: ledgerprojection.Residual{
			LegQuantity:            decimal.RequireFromString("0"),
			ReportingAssetNotional: decimal.RequireFromString("0.00000001"),
			OpenOrderCount:         1,
			PendingCompensation:    2,
			BlockerClass:           "dust",
		},
		Watermark: ledgerprojection.Watermark{Status: ledgerfacts.Complete, Verified: true, CompleteThrough: asOf},
		Conversion: ledgerprojection.ConversionMetadata{
			ConversionSource: "USDT/USD",
			ConversionAsOf:   conversionAt,
			RoundingMode:     "half_even",
			Scale:            8,
			FormulaVersion:   "ledger-v1",
		},
	}
	metadata := closedPositionReadmodelMetadata{
		LineageID: "lineage-1",
		Symbol:    "BTC-USDT",
		LongLeg: closedPositionLegMetadata{
			AccountID: "long-account", Venue: "binance", Symbol: "BTC-USDT",
			InstrumentID: "long-instrument", VenueEnvironment: "testnet",
			GatewayInstrumentID: "gateway-only-long",
		},
		ShortLeg: closedPositionLegMetadata{
			AccountID: "short-account", Venue: "okx", Symbol: "BTC-USDT",
			InstrumentID: "short-instrument", VenueEnvironment: "demo",
			GatewayInstrumentID: "gateway-only-short",
		},
		OpenedAt: openedAt,
		ClosedAt: closedAt,
	}

	dto, err := newClosedPositionLedgerDTO(projection, metadata)
	if err != nil {
		t.Fatal(err)
	}
	body := mustJSON(t, dto)

	assertJSONValue(t, body, "principalId", "principal-1")
	assertJSONValue(t, body, "lineageId", "lineage-1")
	assertJSONValue(t, body, "symbol", "BTC-USDT")
	assertJSONValue(t, body, "longLeg.accountId", "long-account")
	assertJSONValue(t, body, "openedAt", "2026-07-29T07:05:06.000000007Z")
	assertJSONValue(t, body, "closedAt", "2026-07-29T09:04:06.000000007Z")
	assertJSONValue(t, body, "realizedTradingPnl", "10.5")
	assertJSONValue(t, body, "unrealizedTradingPnl", "0")
	assertJSONValue(t, body, "actualFundingCashFlow", "-0.75")
	assertJSONValue(t, body, "fees", "-0.05")
	assertJSONValue(t, body, "netPnl", "9.7")
	assertJSONValue(t, body, "closedTradingUpnl", "0")
	assertJSONValue(t, body, "conversionAsOf", "2026-07-29T09:05:05.000000007Z")
	assertJSONValue(t, body, "residual.reportingAssetNotional", "0.00000001")
	assertJSONValue(t, body, "watermark.verified", true)
	assertJSONOmits(t, body, "gatewayInstrumentId", "gateway-only", "secret", "credential")
}

func TestPortfolioLedgerDTOUsesProjectionFields(t *testing.T) {
	asOf := time.Date(2026, 7, 30, 4, 5, 6, 7, time.UTC)
	dto, err := newPortfolioLedgerDTO(ledgerprojection.PortfolioProjection{
		PrincipalID:              "principal-1",
		Mode:                     "test",
		ReportingAsset:           "USDT",
		AsOf:                     asOf,
		CumulativeRealizedNetPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("42.01")},
		CurrentOpenUnrealizedPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-3.01")},
		TotalNetPnl:              ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("39.00")},
		FundingTotal:             ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1.50")},
		FeeTotal:                 ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.25")},
		OpenPositionCount:        2,
		ClosedPositionCount:      3,
		VerificationStatus:       "provisional",
		Residual:                 ledgerprojection.Residual{OpenOrderCount: 1, BlockerClass: "open_order"},
		Watermark:                ledgerprojection.Watermark{Status: ledgerfacts.Incomplete, CompleteThrough: asOf},
		Conversion:               ledgerprojection.ConversionMetadata{ConversionSource: "USDT/USD", ConversionAsOf: asOf, RoundingMode: "half_even", Scale: 8, FormulaVersion: "ledger-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := mustJSON(t, dto)
	assertJSONValue(t, body, "cumulativeRealizedNetPnl", "42.01")
	assertJSONValue(t, body, "currentOpenUnrealizedPnl", "-3.01")
	assertJSONValue(t, body, "totalNetPnl", "39")
	assertJSONValue(t, body, "actualFundingCashFlow", "1.5")
	assertJSONValue(t, body, "fees", "-0.25")
	assertJSONValue(t, body, "openPositionCount", float64(2))
	assertJSONValue(t, body, "closedPositionCount", float64(3))
	assertJSONValue(t, body, "verificationStatus", "provisional")
	assertJSONValue(t, body, "residual.openOrderCount", float64(1))
}

func TestReadinessDTOExtendsLegacyShape(t *testing.T) {
	dto := newReadinessDTO(readiness.Result{
		Ready: true,
		Components: map[string]readiness.Component{
			"process": {Ready: true},
		},
	}, readiness.TradingReadinessResult{
		OpenReady:     false,
		CloseReady:    true,
		RecoveryReady: true,
		Reason:        readiness.TradingReadinessReasonNormalTestCloseRecovery,
	})
	body := mustJSON(t, dto)
	assertJSONValue(t, body, "ready", true)
	assertJSONValue(t, body, "components.process.ready", true)
	assertJSONValue(t, body, "openReady", false)
	assertJSONValue(t, body, "closeReady", true)
	assertJSONValue(t, body, "recoveryReady", true)
	assertJSONValue(t, body, "readinessReason", "normal_test_close_recovery")
}

func TestVerificationStatusDTOWhitelist(t *testing.T) {
	_, err := newVerificationStatusDTO("raw_gateway_ok", "leak", "details", ledgerprojection.Watermark{}, readiness.TradingReadinessResult{})
	if err == nil {
		t.Fatal("invalid verification status was accepted")
	}
	dto, err := newVerificationStatusDTO("revised", "late_fact", "superseded by fact-2", ledgerprojection.Watermark{
		Status:          ledgerfacts.Complete,
		Verified:        true,
		CompleteThrough: time.Date(2026, 7, 30, 4, 5, 6, 7, time.UTC),
	}, readiness.TradingReadinessResult{OpenReady: true, CloseReady: false, RecoveryReady: true})
	if err != nil {
		t.Fatal(err)
	}
	body := mustJSON(t, dto)
	assertJSONValue(t, body, "status", "revised")
	assertJSONValue(t, body, "watermark.completeThrough", "2026-07-30T04:05:06.000000007Z")
	assertJSONValue(t, body, "openReady", true)
	assertJSONValue(t, body, "closeReady", false)
	assertJSONValue(t, body, "recoveryReady", true)
}

func TestLedgerTimeDTOTreatsDatabaseZeroSentinelAsAbsent(t *testing.T) {
	if got := ledgerTimeDTO(time.Date(1, time.January, 1, 0, 0, 43, 0, time.UTC)); got != "" {
		t.Fatalf("database zero-time sentinel encoded as %q", got)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertJSONOmits(t *testing.T, body []byte, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(string(body), value) {
			t.Fatalf("json body leaked %q: %s", value, string(body))
		}
	}
}

func assertJSONValue(t *testing.T, body []byte, path string, want any) {
	t.Helper()
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	got := jsonPath(root, strings.Split(path, "."))
	if got != want {
		t.Fatalf("%s=%#v want %#v body=%s", path, got, want, string(body))
	}
}

func jsonPath(value any, parts []string) any {
	for _, part := range parts {
		switch item := value.(type) {
		case map[string]any:
			value = item[part]
		case []any:
			if part != "0" {
				return nil
			}
			value = item[0]
		default:
			return nil
		}
	}
	return value
}
