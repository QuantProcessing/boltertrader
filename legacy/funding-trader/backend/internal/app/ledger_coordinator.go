package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

var (
	ErrLedgerReconciliationUnavailable   = errors.New("ledger reconciliation coordinator is unavailable")
	ErrLedgerReconciliationScopeMismatch = errors.New("ledger reconciliation projection scope mismatch")
	ErrLedgerReconciliationEventPublish  = errors.New("ledger reconciliation event publish degraded")
)

type ledgerPositionReconciler interface {
	ReconcileClosedPosition(context.Context, execution.Execution, execution.Execution, string) (ledgerreconcile.ReconcileResult, error)
}

type ledgerReconciliationEventPublisher interface {
	Publish(context.Context, uistream.EventInput) (uistream.StoredEvent, error)
}

type LedgerReconciliationCoordinator struct {
	reconciler  ledgerPositionReconciler
	stream      ledgerReconciliationEventPublisher
	principalID string
	mode        string
}

func NewLedgerReconciliationCoordinator(
	reconciler ledgerPositionReconciler,
	stream ledgerReconciliationEventPublisher,
	principalID string,
	mode string,
) *LedgerReconciliationCoordinator {
	return &LedgerReconciliationCoordinator{
		reconciler:  reconciler,
		stream:      stream,
		principalID: strings.TrimSpace(principalID),
		mode:        strings.TrimSpace(mode),
	}
}

func (coordinator *LedgerReconciliationCoordinator) ReconcileClosedPosition(
	ctx context.Context,
	open execution.Execution,
	close execution.Execution,
	reportingAsset string,
) (ledgerreconcile.ReconcileResult, error) {
	if coordinator == nil ||
		coordinator.reconciler == nil ||
		coordinator.stream == nil ||
		coordinator.principalID == "" ||
		(coordinator.mode != "test" && coordinator.mode != "live") {
		return ledgerreconcile.ReconcileResult{}, ErrLedgerReconciliationUnavailable
	}
	result, reconcileErr := coordinator.reconciler.ReconcileClosedPosition(ctx, open, close, reportingAsset)
	if !hasLedgerProjectionResult(result) {
		return result, reconcileErr
	}
	if err := coordinator.validateResultScope(result); err != nil {
		return result, errors.Join(reconcileErr, err)
	}
	if err := coordinator.publishResult(ctx, result); err != nil {
		return result, errors.Join(reconcileErr, fmt.Errorf("%w: %w", ErrLedgerReconciliationEventPublish, err))
	}
	return result, reconcileErr
}

func hasLedgerProjectionResult(result ledgerreconcile.ReconcileResult) bool {
	return len(result.Closed) > 0 || strings.TrimSpace(result.Portfolio.ReportingAsset) != ""
}

func (coordinator *LedgerReconciliationCoordinator) validateResultScope(result ledgerreconcile.ReconcileResult) error {
	if result.Bridge.PrincipalID != "" &&
		(result.Bridge.PrincipalID != coordinator.principalID || result.Bridge.Mode != coordinator.mode) {
		return ErrLedgerReconciliationScopeMismatch
	}
	for _, closed := range result.Closed {
		if closed.Scope.PrincipalID != coordinator.principalID || closed.Scope.Mode != coordinator.mode {
			return ErrLedgerReconciliationScopeMismatch
		}
	}
	if result.Portfolio.ReportingAsset != "" &&
		(result.Portfolio.PrincipalID != coordinator.principalID || result.Portfolio.Mode != coordinator.mode) {
		return ErrLedgerReconciliationScopeMismatch
	}
	return nil
}

func (coordinator *LedgerReconciliationCoordinator) publishResult(
	ctx context.Context,
	result ledgerreconcile.ReconcileResult,
) error {
	for _, closed := range result.Closed {
		if err := coordinator.publish(ctx, readmodel.LedgerPositionVerificationChanged, closedPositionPayload(closed)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(result.Portfolio.ReportingAsset) != "" {
		if err := coordinator.publish(ctx, readmodel.LedgerPortfolioUpdated, portfolioPayload(result.Portfolio)); err != nil {
			return err
		}
		if err := coordinator.publish(ctx, readmodel.VerificationStatus, verificationPayload(result.Portfolio)); err != nil {
			return err
		}
	}
	return nil
}

func (coordinator *LedgerReconciliationCoordinator) publish(
	ctx context.Context,
	kind readmodel.EventKind,
	payload any,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = coordinator.stream.Publish(ctx, uistream.EventInput{
		Scope:       "all",
		PrincipalID: coordinator.principalID,
		Mode:        coordinator.mode,
		Kind:        string(kind),
		Data:        body,
	})
	return err
}

type ledgerPositionVerificationPayload struct {
	PositionID            string                  `json:"positionId"`
	ReportingAsset        string                  `json:"reportingAsset"`
	AsOf                  time.Time               `json:"asOf"`
	RealizedTradingPnl    ledgerMoneyPayload      `json:"realizedTradingPnl"`
	UnrealizedTradingPnl  ledgerMoneyPayload      `json:"unrealizedTradingPnl"`
	ActualFundingCashFlow ledgerMoneyPayload      `json:"actualFundingCashFlow"`
	Fees                  ledgerMoneyPayload      `json:"fees"`
	NetPnl                ledgerMoneyPayload      `json:"netPnl"`
	ClosedTradingUPnL     ledgerMoneyPayload      `json:"closedTradingUPnL"`
	Verified              bool                    `json:"verified"`
	VerificationStatus    string                  `json:"verificationStatus"`
	Residual              ledgerResidualPayload   `json:"residual"`
	Watermark             ledgerWatermarkPayload  `json:"watermark"`
	Conversion            ledgerConversionPayload `json:"conversion"`
}

type ledgerPortfolioPayload struct {
	ReportingAsset           string                  `json:"reportingAsset"`
	AsOf                     time.Time               `json:"asOf"`
	CumulativeRealizedNetPnl ledgerMoneyPayload      `json:"cumulativeRealizedNetPnl"`
	CurrentOpenUnrealizedPnl ledgerMoneyPayload      `json:"currentOpenUnrealizedPnl"`
	TotalNetPnl              ledgerMoneyPayload      `json:"totalNetPnl"`
	FundingTotal             ledgerMoneyPayload      `json:"fundingTotal"`
	FeeTotal                 ledgerMoneyPayload      `json:"feeTotal"`
	OpenPositionCount        int                     `json:"openPositionCount"`
	ClosedPositionCount      int                     `json:"closedPositionCount"`
	VerificationStatus       string                  `json:"verificationStatus"`
	Residual                 ledgerResidualPayload   `json:"residual"`
	Watermark                ledgerWatermarkPayload  `json:"watermark"`
	Conversion               ledgerConversionPayload `json:"conversion"`
}

type ledgerVerificationStatusPayload struct {
	ReportingAsset      string                 `json:"reportingAsset"`
	AsOf                time.Time              `json:"asOf"`
	VerificationStatus  string                 `json:"verificationStatus"`
	Verified            bool                   `json:"verified"`
	OpenPositionCount   int                    `json:"openPositionCount"`
	ClosedPositionCount int                    `json:"closedPositionCount"`
	Watermark           ledgerWatermarkPayload `json:"watermark"`
}

type ledgerMoneyPayload struct {
	Asset  string `json:"asset"`
	Amount string `json:"amount"`
}

type ledgerResidualPayload struct {
	LegQuantity            string `json:"legQuantity"`
	ReportingAssetNotional string `json:"reportingAssetNotional"`
	OpenOrderCount         int    `json:"openOrderCount"`
	PendingCompensation    int    `json:"pendingCompensation"`
	BlockerClass           string `json:"blockerClass"`
}

type ledgerWatermarkPayload struct {
	Status          string    `json:"status"`
	Verified        bool      `json:"verified"`
	CompleteThrough time.Time `json:"completeThrough"`
}

type ledgerConversionPayload struct {
	ConversionSource string    `json:"conversionSource"`
	ConversionAsOf   time.Time `json:"conversionAsOf"`
	RoundingMode     string    `json:"roundingMode"`
	Scale            int       `json:"scale"`
	FormulaVersion   string    `json:"formulaVersion"`
}

func closedPositionPayload(projection ledgerprojection.ClosedPositionProjection) ledgerPositionVerificationPayload {
	return ledgerPositionVerificationPayload{
		PositionID:            projection.PositionID,
		ReportingAsset:        projection.ReportingAsset,
		AsOf:                  projection.AsOf.UTC(),
		RealizedTradingPnl:    moneyPayload(projection.RealizedTradingPnl),
		UnrealizedTradingPnl:  moneyPayload(projection.UnrealizedTradingPnl),
		ActualFundingCashFlow: moneyPayload(projection.ActualFundingCashFlow),
		Fees:                  moneyPayload(projection.Fees),
		NetPnl:                moneyPayload(projection.NetPnl),
		ClosedTradingUPnL:     moneyPayload(projection.ClosedTradingUPnL),
		Verified:              projection.Verified,
		VerificationStatus:    projection.VerificationStatus,
		Residual:              residualPayload(projection.Residual),
		Watermark:             watermarkPayload(projection.Watermark),
		Conversion:            conversionPayload(projection.Conversion),
	}
}

func portfolioPayload(projection ledgerprojection.PortfolioProjection) ledgerPortfolioPayload {
	return ledgerPortfolioPayload{
		ReportingAsset:           projection.ReportingAsset,
		AsOf:                     projection.AsOf.UTC(),
		CumulativeRealizedNetPnl: moneyPayload(projection.CumulativeRealizedNetPnl),
		CurrentOpenUnrealizedPnl: moneyPayload(projection.CurrentOpenUnrealizedPnl),
		TotalNetPnl:              moneyPayload(projection.TotalNetPnl),
		FundingTotal:             moneyPayload(projection.FundingTotal),
		FeeTotal:                 moneyPayload(projection.FeeTotal),
		OpenPositionCount:        projection.OpenPositionCount,
		ClosedPositionCount:      projection.ClosedPositionCount,
		VerificationStatus:       projection.VerificationStatus,
		Residual:                 residualPayload(projection.Residual),
		Watermark:                watermarkPayload(projection.Watermark),
		Conversion:               conversionPayload(projection.Conversion),
	}
}

func verificationPayload(projection ledgerprojection.PortfolioProjection) ledgerVerificationStatusPayload {
	return ledgerVerificationStatusPayload{
		ReportingAsset:      projection.ReportingAsset,
		AsOf:                projection.AsOf.UTC(),
		VerificationStatus:  projection.VerificationStatus,
		Verified:            projection.VerificationStatus == "verified" && projection.Watermark.Verified,
		OpenPositionCount:   projection.OpenPositionCount,
		ClosedPositionCount: projection.ClosedPositionCount,
		Watermark:           watermarkPayload(projection.Watermark),
	}
}

func moneyPayload(money ledgerfacts.Money) ledgerMoneyPayload {
	return ledgerMoneyPayload{
		Asset:  strings.TrimSpace(money.Asset),
		Amount: decimalString(money.Amount),
	}
}

func residualPayload(residual ledgerprojection.Residual) ledgerResidualPayload {
	return ledgerResidualPayload{
		LegQuantity:            decimalString(residual.LegQuantity),
		ReportingAssetNotional: decimalString(residual.ReportingAssetNotional),
		OpenOrderCount:         residual.OpenOrderCount,
		PendingCompensation:    residual.PendingCompensation,
		BlockerClass:           residual.BlockerClass,
	}
}

func watermarkPayload(watermark ledgerprojection.Watermark) ledgerWatermarkPayload {
	return ledgerWatermarkPayload{
		Status:          string(watermark.Status),
		Verified:        watermark.Verified,
		CompleteThrough: watermark.CompleteThrough.UTC(),
	}
}

func conversionPayload(conversion ledgerprojection.ConversionMetadata) ledgerConversionPayload {
	return ledgerConversionPayload{
		ConversionSource: conversion.ConversionSource,
		ConversionAsOf:   conversion.ConversionAsOf.UTC(),
		RoundingMode:     conversion.RoundingMode,
		Scale:            conversion.Scale,
		FormulaVersion:   conversion.FormulaVersion,
	}
}

func decimalString(value decimal.Decimal) string {
	if value.IsZero() {
		return "0"
	}
	return value.String()
}
