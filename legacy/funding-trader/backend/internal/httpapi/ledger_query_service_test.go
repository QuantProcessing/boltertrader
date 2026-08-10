package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/positioneconomics"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

func TestLedgerQueryServiceFundingPaymentsPreservesServerScopeAndWatermark(t *testing.T) {
	ctx := context.Background()
	observedAt := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	completeThrough := observedAt.Add(-time.Hour)
	from := observedAt.Add(-12 * time.Hour)
	to := observedAt.Add(-time.Hour)
	funding := &fakeLedgerFundingStore{
		items: []ledgerfacts.FundingPaymentFact{{
			FactID: "fact-1", PaymentID: "payment-1",
			Scope: ledgerfacts.Scope{PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP"},
			Provenance: ledgerfacts.Provenance{
				ObservedAt: observedAt.Add(-time.Minute), EffectiveAt: observedAt.Add(-2 * time.Hour),
				SourceRevision: "rev-item", SourceVersion: "v1", SourceRef: "src-1",
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1.25")},
		}},
		watermark: ledgerfacts.SyncWatermark{
			Scope: ledgerfacts.Scope{
				PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
				WindowStart: from, WindowEnd: to,
				PaginationTraversal: "gateway-page-asc",
			},
			Cursor: "cursor-1", Revision: "rev-watermark", ObservedAt: observedAt,
			CompleteThrough: completeThrough, Status: ledgerfacts.Complete, Verified: true,
		},
		watermarkOK: true,
	}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Funding: funding})

	page, err := service.FundingPayments(ctx, ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		PositionID: "position-1", From: from, To: to, Limit: 25,
	})
	if err != nil {
		t.Fatalf("FundingPayments returned error: %v", err)
	}

	if funding.query.PrincipalID != "principal-1" || funding.query.Mode != "test" ||
		funding.query.AccountID != "account-1" || funding.query.InstrumentID != "BTC-USDT-PERP" ||
		funding.query.PositionID != "position-1" || funding.query.Limit != 26 {
		t.Fatalf("funding query lost server-owned scope or filters: %+v", funding.query)
	}
	if funding.watermarkPrincipalID != "principal-1" || funding.watermarkMode != "test" ||
		funding.watermarkAccountID != "account-1" || funding.watermarkInstrumentID != "BTC-USDT-PERP" ||
		!funding.watermarkFrom.Equal(from) || !funding.watermarkTo.Equal(to) {
		t.Fatalf("watermark query lost server-owned scope: %s %s %s %s",
			funding.watermarkPrincipalID, funding.watermarkMode, funding.watermarkAccountID, funding.watermarkInstrumentID)
	}
	if page.Status != string(ledgerfacts.Complete) || page.Watermark.Revision != "rev-watermark" ||
		page.AsOf != ledgerTimeDTO(observedAt) || page.CompleteThrough != ledgerTimeDTO(completeThrough) {
		t.Fatalf("page did not use latest watermark: %+v", page)
	}
	if page.Scope.PrincipalID != "principal-1" || page.Scope.Mode != "test" ||
		page.Scope.AccountID != "account-1" || page.Scope.InstrumentID != "BTC-USDT-PERP" {
		t.Fatalf("page scope = %+v", page.Scope)
	}
}

func TestLedgerQueryServiceFundingPaymentsUsesScopedOpaqueCursorAndNeverInventsCompleteEmpty(t *testing.T) {
	codec, err := readmodel.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 30, 5, 0, 0, 0, time.UTC)
	store := &fakeLedgerFundingStore{items: []ledgerfacts.FundingPaymentFact{
		fundingItem("fact-3", 3, base),
		fundingItem("fact-2", 2, base.Add(-time.Minute)),
		fundingItem("fact-1", 1, base.Add(-2*time.Minute)),
	}}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Funding: store, CursorCodec: codec})
	query := ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1",
		InstrumentID: "BTC-USDT-PERP", Limit: 2,
	}
	first, err := service.FundingPayments(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != string(ledgerfacts.Incomplete) || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	if store.query.Limit != 3 {
		t.Fatalf("store limit=%d, want lookahead 3", store.query.Limit)
	}
	query.Cursor = first.NextCursor
	if _, err := service.FundingPayments(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if !store.query.CursorEffectiveAt.Equal(base.Add(-time.Minute)) || store.query.CursorSequenceID != 2 {
		t.Fatalf("decoded cursor was not bound to the last item: %+v", store.query)
	}

	tampered := query
	tampered.AccountID = "other-account"
	if _, err := service.FundingPayments(context.Background(), tampered); !errors.Is(err, readmodel.ErrInvalidCursor) {
		t.Fatalf("cross-scope cursor err=%v", err)
	}
}

func TestLedgerQueryServiceFundingPaymentsRequiresExactWindowBeforeAttachingWatermark(t *testing.T) {
	base := time.Date(2026, 7, 30, 5, 0, 0, 0, time.UTC)
	store := &fakeLedgerFundingStore{
		items: []ledgerfacts.FundingPaymentFact{fundingItem("fact-1", 1, base)},
		watermark: ledgerfacts.SyncWatermark{
			Scope: ledgerfacts.Scope{
				PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
				WindowStart: base.Add(-time.Hour), WindowEnd: base,
			},
			CompleteThrough: base, Status: ledgerfacts.Complete, Verified: true,
		},
		watermarkOK: true,
	}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Funding: store})

	page, err := service.FundingPayments(context.Background(), ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != string(ledgerfacts.Incomplete) || store.watermarkCalls != 0 {
		t.Fatalf("open-ended query attached finality page=%+v watermarkCalls=%d", page, store.watermarkCalls)
	}
	if page.CompleteThrough != "" || page.Revision != "" || page.Watermark.CompleteThrough != "" {
		t.Fatalf("open-ended query invented coverage evidence: %+v", page)
	}

	page, err = service.FundingPayments(context.Background(), ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		From: base.Add(-time.Hour), To: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != string(ledgerfacts.Complete) || page.CompleteThrough != ledgerTimeDTO(base) || store.watermarkCalls != 1 {
		t.Fatalf("exact query did not attach matching finality page=%+v watermarkCalls=%d", page, store.watermarkCalls)
	}

	store.watermarkOK = false
	page, err = service.FundingPayments(context.Background(), ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		From: base.Add(-time.Hour), To: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != string(ledgerfacts.Incomplete) || page.CompleteThrough != "" || page.Revision != "" ||
		page.Watermark.CompleteThrough != "" || page.Watermark.Revision != "" || store.watermarkCalls != 2 {
		t.Fatalf("exact query without watermark invented finality page=%+v watermarkCalls=%d", page, store.watermarkCalls)
	}

	page, err = service.FundingPayments(context.Background(), ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		To: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != string(ledgerfacts.Incomplete) || page.CompleteThrough != "" || store.watermarkCalls != 2 {
		t.Fatalf("to-only query invented finality page=%+v watermarkCalls=%d", page, store.watermarkCalls)
	}
}

func TestLedgerQueryServiceFundingPaymentsRejectsMismatchedWatermarkWindow(t *testing.T) {
	base := time.Date(2026, 7, 30, 5, 0, 0, 0, time.UTC)
	store := &fakeLedgerFundingStore{
		watermark: ledgerfacts.SyncWatermark{
			Scope: ledgerfacts.Scope{
				PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
				WindowStart: base.Add(-2 * time.Hour), WindowEnd: base,
			},
			CompleteThrough: base, Status: ledgerfacts.Complete, Verified: true,
		},
		watermarkOK: true,
	}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Funding: store})
	_, err := service.FundingPayments(context.Background(), ledgerFundingPaymentQuery{
		PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		From: base.Add(-time.Hour), To: base,
	})
	if !errors.Is(err, ledgerfacts.ErrScopeMismatch) {
		t.Fatalf("mismatched exact watermark err=%v, want ErrScopeMismatch", err)
	}
}

func fundingItem(id string, sequence int64, effectiveAt time.Time) ledgerfacts.FundingPaymentFact {
	return ledgerfacts.FundingPaymentFact{
		FactID: id, PaymentID: id, SequenceID: sequence,
		Scope: ledgerfacts.Scope{
			PrincipalID: "principal-1", Mode: "test", AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		},
		Provenance: ledgerfacts.Provenance{SourceRef: id, SourceRevision: id, SourceVersion: "v1", ObservedAt: effectiveAt, EffectiveAt: effectiveAt},
		Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.NewFromInt(sequence)},
	}
}

func TestLedgerQueryServiceClosedPositionUsesScopedProjectionAndSanitizedReadmodelMetadata(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC)
	projection := closedProjectionFixture("principal-1", "test", "position-1", asOf)
	projections := &fakeLedgerProjectionStore{closed: []ledgerprojection.ClosedPositionProjection{projection}}
	reader := &fakeLedgerPositionReader{positions: map[string]json.RawMessage{
		"position-1": json.RawMessage(`{
			"lineageId":"lineage-1",
			"symbol":"BTC-USDT",
			"longLeg":{"accountId":"long-account","venue":"binance","symbol":"BTC-USDT","instrumentId":"long-inst","venueEnvironment":"demo","gatewayInstrumentId":"private-long"},
			"shortLeg":{"accountId":"short-account","venue":"okx","symbol":"BTC-USDT","instrumentId":"short-inst","venueEnvironment":"testnet","gatewayInstrumentId":"private-short"},
			"openedAt":"2026-07-29T01:00:00Z",
			"closedAt":"2026-07-30T01:00:00Z"
		}`),
	}}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Projection: projections, PositionReader: reader})

	dto, ok, err := service.ClosedPosition(ctx, readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, "position-1")
	if err != nil {
		t.Fatalf("ClosedPosition returned error: %v", err)
	}
	if !ok {
		t.Fatal("ClosedPosition did not find scoped projection")
	}
	if projections.closedPositionPrincipalID != "principal-1" || projections.closedPositionMode != "test" ||
		projections.closedPositionAsset != "USDT" ||
		projections.closedPositionID != "position-1" {
		t.Fatalf("closed position query lost scope: %s %s %s %s", projections.closedPositionPrincipalID, projections.closedPositionMode, projections.closedPositionAsset, projections.closedPositionID)
	}
	if reader.scope.PrincipalID != "principal-1" || reader.scope.Mode != "test" || reader.positionID != "position-1" {
		t.Fatalf("readmodel position query lost scope: %+v %s", reader.scope, reader.positionID)
	}
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal dto: %v", err)
	}
	if jsonContainsKey(body, "gatewayInstrumentId") {
		t.Fatalf("closed position DTO leaked gateway-private metadata: %s", string(body))
	}
	if dto.LineageID != "lineage-1" || dto.LongLeg.AccountID != "long-account" ||
		dto.ShortLeg.InstrumentID != "short-inst" || dto.VerificationStatus != "verified" {
		t.Fatalf("closed position DTO did not merge projection and metadata: %+v", dto)
	}
}

func TestLedgerQueryServiceClosedPositionsAndPortfolioUseServerScopeAndAsset(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	projections := &fakeLedgerProjectionStore{
		closed: []ledgerprojection.ClosedPositionProjection{closedProjectionFixture("principal-1", "live", "position-1", asOf)},
		portfolio: ledgerprojection.PortfolioProjection{
			PrincipalID: "principal-1", Mode: "live", ReportingAsset: "USD", AsOf: asOf,
			CumulativeRealizedNetPnl: ledgerfacts.Money{Asset: "USD", Amount: decimal.RequireFromString("10")},
			CurrentOpenUnrealizedPnl: ledgerfacts.Money{Asset: "USD", Amount: decimal.RequireFromString("0")},
			TotalNetPnl:              ledgerfacts.Money{Asset: "USD", Amount: decimal.RequireFromString("10")},
			FundingTotal:             ledgerfacts.Money{Asset: "USD", Amount: decimal.RequireFromString("1")},
			FeeTotal:                 ledgerfacts.Money{Asset: "USD", Amount: decimal.RequireFromString("-0.5")},
			VerificationStatus:       "verified",
			Watermark:                ledgerprojection.Watermark{Status: ledgerfacts.Complete, Verified: true, CompleteThrough: asOf},
			Conversion:               ledgerprojection.ConversionMetadata{ConversionSource: "USDT/USD", ConversionAsOf: asOf, RoundingMode: "half_even", Scale: 8, FormulaVersion: "ledger-v1"},
		},
		portfolioOK: true,
	}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Projection: projections, ReportingAsset: "USDT"})

	closed, err := service.ClosedPositions(ctx, readmodel.Scope{PrincipalID: "principal-1", Mode: "live"}, "USD")
	if err != nil {
		t.Fatalf("ClosedPositions returned error: %v", err)
	}
	if len(closed) != 1 || projections.closedPrincipalID != "principal-1" || projections.closedMode != "live" || projections.closedAsset != "USD" {
		t.Fatalf("closed positions query did not preserve scope/asset: got=%+v scope=%s/%s/%s", closed, projections.closedPrincipalID, projections.closedMode, projections.closedAsset)
	}
	portfolio, ok, err := service.Portfolio(ctx, readmodel.Scope{PrincipalID: "principal-1", Mode: "live"}, "USD")
	if err != nil || !ok {
		t.Fatalf("Portfolio returned ok=%v err=%v", ok, err)
	}
	if portfolio.ReportingAsset != "USD" || portfolio.TotalNetPnl == nil || *portfolio.TotalNetPnl != "10" ||
		projections.portfolioPrincipalID != "principal-1" || projections.portfolioMode != "live" || projections.portfolioAsset != "USD" {
		t.Fatalf("portfolio did not preserve scope/asset: dto=%+v query=%s/%s/%s", portfolio, projections.portfolioPrincipalID, projections.portfolioMode, projections.portfolioAsset)
	}
}

func TestLedgerQueryServicePortfolioOverlaysAuthoritativeOpenPositionEconomics(t *testing.T) {
	ctx := context.Background()
	projectionAsOf := time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC)
	liveAsOf := projectionAsOf.Add(time.Minute)
	projections := &fakeLedgerProjectionStore{
		portfolio: ledgerprojection.PortfolioProjection{
			PrincipalID: "principal-1", Mode: "test", ReportingAsset: "USDT", AsOf: projectionAsOf,
			CumulativeRealizedNetPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.14194303")},
			CurrentOpenUnrealizedPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
			TotalNetPnl:              ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.14194303")},
			VerificationStatus:       "provisional",
		},
		portfolioOK: true,
	}
	live := &fakeLivePositionEconomics{snapshot: positioneconomics.PortfolioSnapshot{
		UnrealizedPnl: decimal.RequireFromString("0.013"), OpenPositionCount: 1,
		AsOf: liveAsOf, Available: true,
	}}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{
		Projection: projections, PositionEconomics: live, ReportingAsset: "USDT",
	})

	portfolio, ok, err := service.Portfolio(ctx, readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, "USDT")
	if err != nil || !ok {
		t.Fatalf("Portfolio returned ok=%v err=%v", ok, err)
	}
	if portfolio.CurrentOpenUnrealizedPnl == nil || *portfolio.CurrentOpenUnrealizedPnl != "0.013" ||
		portfolio.TotalNetPnl == nil || *portfolio.TotalNetPnl != "-0.12894303" ||
		portfolio.OpenPositionCount != 1 || portfolio.AsOf != liveAsOf.Format(time.RFC3339Nano) ||
		portfolio.LiveEconomicsStatus != "available" {
		t.Fatalf("portfolio did not overlay live economics: %+v", portfolio)
	}
	if live.scope.PrincipalID != "principal-1" || live.scope.Mode != "test" || live.asset != "USDT" {
		t.Fatalf("live economics query lost scope: %+v asset=%s", live.scope, live.asset)
	}
}

func TestLedgerQueryServicePortfolioDoesNotFabricateZeroForIncompleteLiveEconomics(t *testing.T) {
	projections := &fakeLedgerProjectionStore{
		portfolio: ledgerprojection.PortfolioProjection{
			PrincipalID: "principal-1", Mode: "test", ReportingAsset: "USDT",
			CumulativeRealizedNetPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("2")},
			CurrentOpenUnrealizedPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
			TotalNetPnl:              ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("2")},
			VerificationStatus:       "provisional",
		},
		portfolioOK: true,
	}
	live := &fakeLivePositionEconomics{snapshot: positioneconomics.PortfolioSnapshot{
		OpenPositionCount: 1, AsOf: time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC), Available: false,
	}}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{Projection: projections, PositionEconomics: live})

	portfolio, ok, err := service.Portfolio(context.Background(), readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, "USDT")
	if err != nil || !ok {
		t.Fatalf("Portfolio returned ok=%v err=%v", ok, err)
	}
	if portfolio.CurrentOpenUnrealizedPnl != nil || portfolio.TotalNetPnl != nil ||
		portfolio.OpenPositionCount != 1 || portfolio.LiveEconomicsStatus != "incomplete" {
		t.Fatalf("incomplete economics fabricated current values: %+v", portfolio)
	}
}

func TestLedgerQueryServiceVerificationUsesProjectionWatermarkAndTradingReadiness(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 7, 30, 4, 0, 0, 0, time.UTC)
	projections := &fakeLedgerProjectionStore{
		watermark:   ledgerprojection.Watermark{Status: ledgerfacts.Complete, Verified: true, CompleteThrough: asOf},
		watermarkOK: true,
	}
	service := NewLedgerQueryService(LedgerQueryServiceConfig{
		Projection:     projections,
		ReportingAsset: "USD",
		TradingReadiness: fakeLedgerTradingReadiness{
			result: readiness.TradingReadinessResult{OpenReady: true, CloseReady: false, RecoveryReady: true, Reason: readiness.TradingReadinessReasonNormalTestOpenPath},
		},
	})

	dto, err := service.Verification(ctx, readmodel.Scope{PrincipalID: "principal-1", Mode: "test"})
	if err != nil {
		t.Fatalf("Verification returned error: %v", err)
	}
	if projections.watermarkPrincipalID != "principal-1" || projections.watermarkMode != "test" || projections.watermarkAsset != "USD" {
		t.Fatalf("verification watermark query lost scope: %s/%s/%s", projections.watermarkPrincipalID, projections.watermarkMode, projections.watermarkAsset)
	}
	if dto.Status != "verified" || dto.Reason != string(readiness.TradingReadinessReasonNormalTestOpenPath) ||
		!dto.OpenReady || dto.CloseReady || !dto.RecoveryReady || dto.Watermark.CompleteThrough != ledgerTimeDTO(asOf) {
		t.Fatalf("verification dto = %+v", dto)
	}
}

func closedProjectionFixture(principalID, mode, positionID string, asOf time.Time) ledgerprojection.ClosedPositionProjection {
	return ledgerprojection.ClosedPositionProjection{
		PositionID: positionID,
		Scope: ledgerfacts.Scope{
			PrincipalID: principalID, Mode: mode, AccountID: "account-1", InstrumentID: "BTC-USDT-PERP",
		},
		ReportingAsset:        "USDT",
		AsOf:                  asOf,
		RealizedTradingPnl:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("12")},
		UnrealizedTradingPnl:  ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		ActualFundingCashFlow: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1")},
		Fees:                  ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.25")},
		NetPnl:                ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("10.75")},
		ClosedTradingUPnL:     ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		VerificationStatus:    "verified",
		Watermark:             ledgerprojection.Watermark{Status: ledgerfacts.Complete, Verified: true, CompleteThrough: asOf},
		Conversion:            ledgerprojection.ConversionMetadata{ConversionSource: "USDT/USD", ConversionAsOf: asOf, RoundingMode: "half_even", Scale: 8, FormulaVersion: "ledger-v1"},
	}
}

type fakeLedgerFundingStore struct {
	query ledgerfacts.FundingPaymentQuery
	items []ledgerfacts.FundingPaymentFact
	err   error

	watermark             ledgerfacts.SyncWatermark
	watermarkOK           bool
	watermarkErr          error
	watermarkPrincipalID  string
	watermarkMode         string
	watermarkAccountID    string
	watermarkInstrumentID string
	watermarkFrom         time.Time
	watermarkTo           time.Time
	watermarkCalls        int
}

func (store *fakeLedgerFundingStore) FundingPayments(_ context.Context, query ledgerfacts.FundingPaymentQuery) ([]ledgerfacts.FundingPaymentFact, error) {
	store.query = query
	return append([]ledgerfacts.FundingPaymentFact(nil), store.items...), store.err
}

func (store *fakeLedgerFundingStore) SyncWatermarkForFundingQuery(_ context.Context, query ledgerfacts.FundingPaymentQuery) (ledgerfacts.SyncWatermark, bool, error) {
	store.watermarkCalls++
	store.watermarkPrincipalID = query.PrincipalID
	store.watermarkMode = query.Mode
	store.watermarkAccountID = query.AccountID
	store.watermarkInstrumentID = query.InstrumentID
	store.watermarkFrom = query.From
	store.watermarkTo = query.To
	return store.watermark, store.watermarkOK, store.watermarkErr
}

type fakeLedgerProjectionStore struct {
	closed []ledgerprojection.ClosedPositionProjection

	closedPrincipalID string
	closedMode        string
	closedAsset       string

	closedPositionPrincipalID string
	closedPositionMode        string
	closedPositionAsset       string
	closedPositionID          string

	portfolio            ledgerprojection.PortfolioProjection
	portfolioOK          bool
	portfolioPrincipalID string
	portfolioMode        string
	portfolioAsset       string

	watermark            ledgerprojection.Watermark
	watermarkOK          bool
	watermarkPrincipalID string
	watermarkMode        string
	watermarkAsset       string
}

func (store *fakeLedgerProjectionStore) ClosedPositions(_ context.Context, principalID, mode, asset string) ([]ledgerprojection.ClosedPositionProjection, error) {
	store.closedPrincipalID = principalID
	store.closedMode = mode
	store.closedAsset = asset
	return append([]ledgerprojection.ClosedPositionProjection(nil), store.closed...), nil
}

func (store *fakeLedgerProjectionStore) ClosedPositionForScope(_ context.Context, principalID, mode, asset, positionID string) (ledgerprojection.ClosedPositionProjection, bool, error) {
	store.closedPositionPrincipalID = principalID
	store.closedPositionMode = mode
	store.closedPositionAsset = asset
	store.closedPositionID = positionID
	for _, item := range store.closed {
		if item.PositionID == positionID && item.Scope.PrincipalID == principalID && item.Scope.Mode == mode && item.ReportingAsset == asset {
			return item, true, nil
		}
	}
	return ledgerprojection.ClosedPositionProjection{}, false, nil
}

func (store *fakeLedgerProjectionStore) Portfolio(_ context.Context, principalID, mode, asset string) (ledgerprojection.PortfolioProjection, bool, error) {
	store.portfolioPrincipalID = principalID
	store.portfolioMode = mode
	store.portfolioAsset = asset
	return store.portfolio, store.portfolioOK, nil
}

func (store *fakeLedgerProjectionStore) Watermark(_ context.Context, principalID, mode, asset string) (ledgerprojection.Watermark, bool, error) {
	store.watermarkPrincipalID = principalID
	store.watermarkMode = mode
	store.watermarkAsset = asset
	return store.watermark, store.watermarkOK, nil
}

type fakeLedgerPositionReader struct {
	positions  map[string]json.RawMessage
	err        error
	scope      readmodel.Scope
	positionID string
}

func (reader *fakeLedgerPositionReader) Position(_ context.Context, scope readmodel.Scope, positionID string) (json.RawMessage, error) {
	reader.scope = scope
	reader.positionID = positionID
	if reader.err != nil {
		return nil, reader.err
	}
	payload, ok := reader.positions[positionID]
	if !ok {
		return nil, readmodel.ErrNotFound
	}
	return payload, nil
}

type fakeLedgerTradingReadiness struct {
	result readiness.TradingReadinessResult
}

type fakeLivePositionEconomics struct {
	snapshot positioneconomics.PortfolioSnapshot
	err      error
	scope    readmodel.Scope
	asset    string
}

func (fake *fakeLivePositionEconomics) Portfolio(_ context.Context, scope readmodel.Scope, asset string) (positioneconomics.PortfolioSnapshot, error) {
	fake.scope = scope
	fake.asset = asset
	return fake.snapshot, fake.err
}

func (fake fakeLedgerTradingReadiness) CheckTrading(context.Context) readiness.TradingReadinessResult {
	return fake.result
}

func jsonContainsKey(body []byte, key string) bool {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return false
	}
	return containsMapKey(value, key)
}

func containsMapKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for candidate, child := range typed {
			if candidate == key || containsMapKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsMapKey(child, key) {
				return true
			}
		}
	}
	return false
}

func TestLedgerQueryServiceIgnoresMissingReadmodelPositionMetadata(t *testing.T) {
	service := NewLedgerQueryService(LedgerQueryServiceConfig{
		Projection:     &fakeLedgerProjectionStore{closed: []ledgerprojection.ClosedPositionProjection{closedProjectionFixture("principal-1", "test", "position-1", time.Now().UTC())}},
		PositionReader: &fakeLedgerPositionReader{err: readmodel.ErrNotFound},
	})
	dto, ok, err := service.ClosedPosition(context.Background(), readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, "position-1")
	if err != nil || !ok {
		t.Fatalf("ClosedPosition with missing metadata returned ok=%v err=%v", ok, err)
	}
	if dto.PositionID != "position-1" || dto.LineageID != "" {
		t.Fatalf("missing metadata should still return projection-only DTO: %+v", dto)
	}
}

func TestLedgerQueryServicePropagatesInvalidReadmodelPositionMetadata(t *testing.T) {
	service := NewLedgerQueryService(LedgerQueryServiceConfig{
		Projection:     &fakeLedgerProjectionStore{closed: []ledgerprojection.ClosedPositionProjection{closedProjectionFixture("principal-1", "test", "position-1", time.Now().UTC())}},
		PositionReader: &fakeLedgerPositionReader{err: errors.New("read failed")},
	})
	_, _, err := service.ClosedPosition(context.Background(), readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, "position-1")
	if err == nil {
		t.Fatal("ClosedPosition did not propagate readmodel metadata error")
	}
}
