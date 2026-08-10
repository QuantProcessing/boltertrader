package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

func TestLedgerReconciliationCoordinatorPublishesSafeEventsForFundingNotFinalResult(t *testing.T) {
	asOf := time.Date(2026, 7, 29, 12, 30, 0, 0, time.UTC)
	reconciler := &ledgerCoordinatorReconcilerFake{
		result: ledgerCoordinatorResultFixture(asOf),
		err:    ledgerreconcile.ErrFundingNotFinal,
	}
	stream := &ledgerCoordinatorStreamFake{}
	coordinator := NewLedgerReconciliationCoordinator(reconciler, stream, "principal-1", "test")

	result, err := coordinator.ReconcileClosedPosition(context.Background(), ledgerCoordinatorOpenExecution(), ledgerCoordinatorCloseExecution(), "USDT")

	if !errors.Is(err, ledgerreconcile.ErrFundingNotFinal) {
		t.Fatalf("err=%v, want ErrFundingNotFinal", err)
	}
	if len(result.Closed) != 1 || result.Portfolio.ReportingAsset != "USDT" {
		t.Fatalf("result was not returned: %+v", result)
	}
	if len(stream.inputs) != 3 {
		t.Fatalf("published %d events: %+v", len(stream.inputs), stream.inputs)
	}
	wantKinds := []string{
		string(readmodel.LedgerPositionVerificationChanged),
		string(readmodel.LedgerPortfolioUpdated),
		string(readmodel.VerificationStatus),
	}
	for i, want := range wantKinds {
		input := stream.inputs[i]
		if input.Scope != "all" || input.PrincipalID != "principal-1" || input.Mode != "test" || input.Kind != want {
			t.Fatalf("event %d input=%+v want kind %q bound to principal/mode", i, input, want)
		}
		if !json.Valid(input.Data) {
			t.Fatalf("event %d has invalid json: %s", i, input.Data)
		}
		assertNoUnsafeLedgerPayload(t, input.Data)
	}

	var position struct {
		PositionID         string `json:"positionId"`
		ReportingAsset     string `json:"reportingAsset"`
		VerificationStatus string `json:"verificationStatus"`
		Verified           bool   `json:"verified"`
		NetPnl             struct {
			Asset  string `json:"asset"`
			Amount string `json:"amount"`
		} `json:"netPnl"`
		Watermark struct {
			Status          string    `json:"status"`
			Verified        bool      `json:"verified"`
			CompleteThrough time.Time `json:"completeThrough"`
		} `json:"watermark"`
	}
	if err := json.Unmarshal(stream.inputs[0].Data, &position); err != nil {
		t.Fatal(err)
	}
	if position.PositionID != "position-1" || position.ReportingAsset != "USDT" ||
		position.VerificationStatus != "provisional" || position.Verified ||
		position.NetPnl.Asset != "USDT" || position.NetPnl.Amount != "7.5" ||
		!position.Watermark.CompleteThrough.Equal(asOf) {
		t.Fatalf("position payload=%+v", position)
	}

	var verification struct {
		ReportingAsset      string `json:"reportingAsset"`
		VerificationStatus  string `json:"verificationStatus"`
		Verified            bool   `json:"verified"`
		ClosedPositionCount int    `json:"closedPositionCount"`
	}
	if err := json.Unmarshal(stream.inputs[2].Data, &verification); err != nil {
		t.Fatal(err)
	}
	if verification.ReportingAsset != "USDT" || verification.VerificationStatus != "provisional" ||
		verification.Verified || verification.ClosedPositionCount != 1 {
		t.Fatalf("verification payload=%+v", verification)
	}
}

func TestLedgerReconciliationCoordinatorDoesNotPublishHardErrorWithoutProjectionResult(t *testing.T) {
	hardErr := errors.New("database unavailable with gateway-instrument-secret")
	reconciler := &ledgerCoordinatorReconcilerFake{err: hardErr}
	stream := &ledgerCoordinatorStreamFake{}
	coordinator := NewLedgerReconciliationCoordinator(reconciler, stream, "principal-1", "test")

	_, err := coordinator.ReconcileClosedPosition(context.Background(), ledgerCoordinatorOpenExecution(), ledgerCoordinatorCloseExecution(), "USDT")

	if !errors.Is(err, hardErr) {
		t.Fatalf("err=%v, want hard err", err)
	}
	if len(stream.inputs) != 0 {
		t.Fatalf("hard error without result published events: %+v", stream.inputs)
	}
}

func TestLedgerReconciliationCoordinatorReturnsDiagnosticDegradationWhenEventPublishFails(t *testing.T) {
	publishErr := errors.New("stream unavailable")
	reconciler := &ledgerCoordinatorReconcilerFake{result: ledgerCoordinatorResultFixture(time.Now().UTC())}
	stream := &ledgerCoordinatorStreamFake{err: publishErr}
	coordinator := NewLedgerReconciliationCoordinator(reconciler, stream, "principal-1", "test")

	_, err := coordinator.ReconcileClosedPosition(context.Background(), ledgerCoordinatorOpenExecution(), ledgerCoordinatorCloseExecution(), "USDT")

	if !errors.Is(err, ErrLedgerReconciliationEventPublish) || !errors.Is(err, publishErr) {
		t.Fatalf("err=%v, want publish degradation", err)
	}
	if reconciler.calls != 1 || len(stream.inputs) != 1 {
		t.Fatalf("calls=%d published=%d", reconciler.calls, len(stream.inputs))
	}
}

func TestLedgerReconciliationCoordinatorRejectsProjectionScopeMismatchBeforePublish(t *testing.T) {
	result := ledgerCoordinatorResultFixture(time.Now().UTC())
	result.Portfolio.PrincipalID = "principal-other"
	reconciler := &ledgerCoordinatorReconcilerFake{result: result}
	stream := &ledgerCoordinatorStreamFake{}
	coordinator := NewLedgerReconciliationCoordinator(reconciler, stream, "principal-1", "test")

	_, err := coordinator.ReconcileClosedPosition(context.Background(), ledgerCoordinatorOpenExecution(), ledgerCoordinatorCloseExecution(), "USDT")

	if !errors.Is(err, ErrLedgerReconciliationScopeMismatch) {
		t.Fatalf("err=%v, want scope mismatch", err)
	}
	if len(stream.inputs) != 0 {
		t.Fatalf("scope mismatch published events: %+v", stream.inputs)
	}
}

func ledgerCoordinatorResultFixture(asOf time.Time) ledgerreconcile.ReconcileResult {
	scope := ledgerfacts.Scope{
		PrincipalID:         "principal-1",
		Mode:                "test",
		AccountID:           "gateway-account-secret",
		InstrumentID:        "BTC-USDT-PERP",
		WindowStart:         asOf.Add(-time.Hour),
		WindowEnd:           asOf,
		PaginationTraversal: "gateway:cursor-secret",
	}
	conversion := ledgerprojection.ConversionMetadata{
		ConversionSource: "passthrough",
		ConversionAsOf:   asOf,
		RoundingMode:     "decimal",
		Scale:            8,
		FormulaVersion:   "ledger-v1",
	}
	watermark := ledgerprojection.Watermark{
		Status:          ledgerfacts.Incomplete,
		Verified:        false,
		CompleteThrough: asOf,
	}
	closed := ledgerprojection.ClosedPositionProjection{
		PositionID:            "position-1",
		Scope:                 scope,
		ReportingAsset:        "USDT",
		AsOf:                  asOf,
		RealizedTradingPnl:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("10")},
		UnrealizedTradingPnl:  ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		ActualFundingCashFlow: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1.5")},
		Fees:                  ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1")},
		NetPnl:                ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("7.5")},
		ClosedTradingUPnL:     ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		Verified:              false,
		VerificationStatus:    "provisional",
		Residual: ledgerprojection.Residual{
			LegQuantity:            decimal.Zero,
			ReportingAssetNotional: decimal.RequireFromString("0.01"),
			OpenOrderCount:         0,
			PendingCompensation:    0,
			BlockerClass:           "funding_not_final",
		},
		Watermark:  watermark,
		Conversion: conversion,
	}
	portfolio := ledgerprojection.PortfolioProjection{
		PrincipalID:              "principal-1",
		Mode:                     "test",
		ReportingAsset:           "USDT",
		AsOf:                     asOf,
		CumulativeRealizedNetPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("7.5")},
		CurrentOpenUnrealizedPnl: ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		TotalNetPnl:              ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("7.5")},
		FundingTotal:             ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1.5")},
		FeeTotal:                 ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-1")},
		ClosedPositionCount:      1,
		VerificationStatus:       "provisional",
		Watermark:                watermark,
		Conversion:               conversion,
	}
	return ledgerreconcile.ReconcileResult{
		Bridge: ledgerreconcile.Bridge{
			PrincipalID:    "principal-1",
			Mode:           "test",
			PositionID:     "position-1",
			ReportingAsset: "USDT",
		},
		Closed:    []ledgerprojection.ClosedPositionProjection{closed},
		Portfolio: portfolio,
	}
}

func ledgerCoordinatorOpenExecution() execution.Execution {
	return execution.Execution{
		ID:         "open-1",
		PositionID: "position-1",
	}
}

func ledgerCoordinatorCloseExecution() execution.Execution {
	return execution.Execution{
		ID:                "close-1",
		ParentExecutionID: "open-1",
		PositionID:        "position-1",
	}
}

func assertNoUnsafeLedgerPayload(t *testing.T, data json.RawMessage) {
	t.Helper()
	raw := string(data)
	for _, forbidden := range []string{
		"BTC-USDT-PERP",
		"gateway-account-secret",
		"gateway:cursor-secret",
		"instrumentId",
		"accountId",
		"credential",
		"token",
		"secret",
		"database unavailable",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("payload leaked %q: %s", forbidden, raw)
		}
	}
}

type ledgerCoordinatorReconcilerFake struct {
	calls          int
	open           execution.Execution
	close          execution.Execution
	reportingAsset string
	result         ledgerreconcile.ReconcileResult
	err            error
}

func (fake *ledgerCoordinatorReconcilerFake) ReconcileClosedPosition(_ context.Context, open, close execution.Execution, reportingAsset string) (ledgerreconcile.ReconcileResult, error) {
	fake.calls++
	fake.open = open
	fake.close = close
	fake.reportingAsset = reportingAsset
	return fake.result, fake.err
}

type ledgerCoordinatorStreamFake struct {
	inputs []uistream.EventInput
	err    error
}

func (fake *ledgerCoordinatorStreamFake) Publish(_ context.Context, input uistream.EventInput) (uistream.StoredEvent, error) {
	fake.inputs = append(fake.inputs, input)
	if fake.err != nil {
		return uistream.StoredEvent{}, fake.err
	}
	return uistream.StoredEvent{Sequence: uint64(len(fake.inputs)), Cursor: "cursor"}, nil
}
