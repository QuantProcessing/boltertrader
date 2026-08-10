package httpapi

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/shopspring/decimal"
)

type fundingPaymentPage struct {
	Items           []ledgerfacts.FundingPaymentFact
	NextCursor      string
	Scope           ledgerfacts.Scope
	Watermark       ledgerfacts.SyncWatermark
	Status          ledgerfacts.CompletenessStatus
	AsOf            time.Time
	CompleteThrough time.Time
	Revision        string
}

type fundingPaymentPageDTO struct {
	Items               []fundingPaymentDTO     `json:"items"`
	NextCursor          string                  `json:"nextCursor,omitempty"`
	Scope               ledgerScopeDTO          `json:"scope"`
	Start               string                  `json:"start"`
	End                 string                  `json:"end"`
	PaginationTraversal string                  `json:"paginationTraversal"`
	Watermark           ledgerFactsWatermarkDTO `json:"watermark"`
	Status              string                  `json:"status"`
	AccountID           string                  `json:"accountId"`
	InstrumentID        string                  `json:"instrumentId,omitempty"`
	AsOf                string                  `json:"asOf"`
	CompleteThrough     string                  `json:"completeThrough"`
	Revision            string                  `json:"revision"`
}

type fundingPaymentDTO struct {
	PaymentID        string `json:"paymentId"`
	FactID           string `json:"factId"`
	SupersedesFactID string `json:"supersedesFactId,omitempty"`
	AccountID        string `json:"accountId"`
	InstrumentID     string `json:"instrumentId"`
	Venue            string `json:"venue"`
	VenueEnvironment string `json:"venueEnvironment"`
	Product          string `json:"product"`
	SettlementAsset  string `json:"settlementAsset"`
	Amount           string `json:"amount"`
	EffectiveAt      string `json:"effectiveAt"`
	ObservedAt       string `json:"observedAt"`
	Revision         string `json:"revision"`
	Version          string `json:"version"`
	SourceRef        string `json:"sourceRef"`
	FormulaVersion   string `json:"formulaVersion"`
}

type ledgerScopeDTO struct {
	PrincipalID         string `json:"principalId,omitempty"`
	Mode                string `json:"mode,omitempty"`
	AccountID           string `json:"accountId"`
	InstrumentID        string `json:"instrumentId,omitempty"`
	Start               string `json:"start"`
	End                 string `json:"end"`
	PaginationTraversal string `json:"paginationTraversal"`
}

type ledgerFactsWatermarkDTO struct {
	Cursor           string `json:"cursor,omitempty"`
	Revision         string `json:"revision,omitempty"`
	ObservedAt       string `json:"observedAt"`
	EffectiveThrough string `json:"effectiveThrough"`
	CompleteThrough  string `json:"completeThrough"`
	Verified         bool   `json:"verified"`
	Status           string `json:"status,omitempty"`
}

type ledgerWatermarkDTO struct {
	Status          string `json:"status"`
	Verified        bool   `json:"verified"`
	CompleteThrough string `json:"completeThrough"`
}

type closedPositionReadmodelMetadata struct {
	LineageID string                    `json:"lineageId"`
	Symbol    string                    `json:"symbol"`
	LongLeg   closedPositionLegMetadata `json:"longLeg"`
	ShortLeg  closedPositionLegMetadata `json:"shortLeg"`
	OpenedAt  time.Time                 `json:"openedAt"`
	ClosedAt  time.Time                 `json:"closedAt"`
}

type closedPositionLegMetadata struct {
	AccountID           string          `json:"accountId"`
	Venue               string          `json:"venue"`
	Symbol              string          `json:"symbol"`
	InstrumentID        string          `json:"instrumentId"`
	VenueEnvironment    string          `json:"venueEnvironment"`
	GatewayInstrumentID string          `json:"gatewayInstrumentId"`
	InitialQuantity     decimal.Decimal `json:"initialQuantity"`
	CurrentQuantity     decimal.Decimal `json:"currentQuantity"`
}

type closedPositionLedgerDTO struct {
	PositionID            string               `json:"positionId"`
	LineageID             string               `json:"lineageId,omitempty"`
	PrincipalID           string               `json:"principalId"`
	Mode                  string               `json:"mode"`
	Symbol                string               `json:"symbol,omitempty"`
	LongLeg               closedPositionLegDTO `json:"longLeg"`
	ShortLeg              closedPositionLegDTO `json:"shortLeg"`
	OpenedAt              string               `json:"openedAt,omitempty"`
	ClosedAt              string               `json:"closedAt,omitempty"`
	ReportingAsset        string               `json:"reportingAsset"`
	VerificationStatus    string               `json:"verificationStatus"`
	RealizedTradingPnl    string               `json:"realizedTradingPnl"`
	UnrealizedTradingPnl  string               `json:"unrealizedTradingPnl"`
	ActualFundingCashFlow string               `json:"actualFundingCashFlow"`
	Fees                  string               `json:"fees"`
	NetPnl                string               `json:"netPnl"`
	ConversionSource      string               `json:"conversionSource"`
	ConversionAsOf        string               `json:"conversionAsOf"`
	RoundingMode          string               `json:"roundingMode"`
	Scale                 int                  `json:"scale"`
	FormulaVersion        string               `json:"formulaVersion"`
	ClosedTradingUPnL     string               `json:"closedTradingUpnl"`
	Residual              ledgerResidualDTO    `json:"residual"`
	Watermark             ledgerWatermarkDTO   `json:"watermark"`
}

type closedPositionLegDTO struct {
	AccountID        string `json:"accountId,omitempty"`
	Venue            string `json:"venue,omitempty"`
	Symbol           string `json:"symbol,omitempty"`
	InstrumentID     string `json:"instrumentId,omitempty"`
	VenueEnvironment string `json:"venueEnvironment,omitempty"`
}

type portfolioLedgerDTO struct {
	PrincipalID              string             `json:"principalId"`
	Mode                     string             `json:"mode"`
	AsOf                     string             `json:"asOf"`
	ReportingAsset           string             `json:"reportingAsset"`
	CumulativeRealizedNetPnl string             `json:"cumulativeRealizedNetPnl"`
	CurrentOpenUnrealizedPnl *string            `json:"currentOpenUnrealizedPnl"`
	TotalNetPnl              *string            `json:"totalNetPnl"`
	LiveEconomicsStatus      string             `json:"liveEconomicsStatus,omitempty"`
	ActualFundingCashFlow    string             `json:"actualFundingCashFlow"`
	Fees                     string             `json:"fees"`
	OpenPositionCount        int                `json:"openPositionCount"`
	ClosedPositionCount      int                `json:"closedPositionCount"`
	VerificationStatus       string             `json:"verificationStatus"`
	ConversionSource         string             `json:"conversionSource"`
	ConversionAsOf           string             `json:"conversionAsOf"`
	RoundingMode             string             `json:"roundingMode"`
	Scale                    int                `json:"scale"`
	FormulaVersion           string             `json:"formulaVersion"`
	Residual                 ledgerResidualDTO  `json:"residual"`
	Watermark                ledgerWatermarkDTO `json:"watermark"`
}

type ledgerResidualDTO struct {
	LegQuantity            string `json:"legQuantity"`
	ReportingAssetNotional string `json:"reportingAssetNotional"`
	OpenOrderCount         int    `json:"openOrderCount"`
	PendingCompensation    int    `json:"pendingCompensation"`
	BlockerClass           string `json:"blockerClass"`
}

func newFundingPaymentPageDTO(page fundingPaymentPage) fundingPaymentPageDTO {
	items := make([]fundingPaymentDTO, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, newFundingPaymentDTO(item))
	}
	return fundingPaymentPageDTO{
		Items:               items,
		NextCursor:          page.NextCursor,
		Scope:               newLedgerScopeDTO(page.Scope),
		Start:               ledgerTimeDTO(page.Scope.WindowStart),
		End:                 ledgerTimeDTO(page.Scope.WindowEnd),
		PaginationTraversal: page.Scope.PaginationTraversal,
		Watermark:           newLedgerFactsWatermarkDTO(page.Watermark),
		Status:              string(page.Status),
		AccountID:           page.Scope.AccountID,
		InstrumentID:        instrumentIDDTO(page.Scope.InstrumentID),
		AsOf:                ledgerTimeDTO(page.AsOf),
		CompleteThrough:     ledgerTimeDTO(page.CompleteThrough),
		Revision:            page.Revision,
	}
}

func newFundingPaymentDTO(item ledgerfacts.FundingPaymentFact) fundingPaymentDTO {
	return fundingPaymentDTO{
		PaymentID:        item.PaymentID,
		FactID:           item.FactID,
		SupersedesFactID: item.SupersedesFactID,
		AccountID:        item.Scope.AccountID,
		InstrumentID:     item.Scope.InstrumentID,
		Venue:            item.Venue,
		VenueEnvironment: item.VenueEnvironment,
		Product:          item.Product,
		SettlementAsset:  item.Payment.Asset,
		Amount:           item.Payment.Amount.String(),
		EffectiveAt:      ledgerTimeDTO(item.Provenance.EffectiveAt),
		ObservedAt:       ledgerTimeDTO(item.Provenance.ObservedAt),
		Revision:         item.Provenance.SourceRevision,
		Version:          item.Provenance.SourceVersion,
		SourceRef:        item.Provenance.SourceRef,
		FormulaVersion:   item.FormulaVersion,
	}
}

func newClosedPositionLedgerDTO(
	projection ledgerprojection.ClosedPositionProjection,
	metadata closedPositionReadmodelMetadata,
) (closedPositionLedgerDTO, error) {
	status := normalizeLedgerVerificationStatus(projection.VerificationStatus)
	if !validLedgerVerificationStatus(status) {
		return closedPositionLedgerDTO{}, errors.New("invalid ledger verification status")
	}
	return closedPositionLedgerDTO{
		PositionID:            projection.PositionID,
		LineageID:             metadata.LineageID,
		PrincipalID:           projection.Scope.PrincipalID,
		Mode:                  projection.Scope.Mode,
		Symbol:                metadata.Symbol,
		LongLeg:               newClosedPositionLegDTO(metadata.LongLeg),
		ShortLeg:              newClosedPositionLegDTO(metadata.ShortLeg),
		OpenedAt:              ledgerTimeDTO(metadata.OpenedAt),
		ClosedAt:              ledgerTimeDTO(metadata.ClosedAt),
		ReportingAsset:        projection.ReportingAsset,
		VerificationStatus:    status,
		RealizedTradingPnl:    projection.RealizedTradingPnl.Amount.String(),
		UnrealizedTradingPnl:  projection.UnrealizedTradingPnl.Amount.String(),
		ActualFundingCashFlow: projection.ActualFundingCashFlow.Amount.String(),
		Fees:                  projection.Fees.Amount.String(),
		NetPnl:                projection.NetPnl.Amount.String(),
		ConversionSource:      projection.Conversion.ConversionSource,
		ConversionAsOf:        ledgerTimeDTO(projection.Conversion.ConversionAsOf),
		RoundingMode:          projection.Conversion.RoundingMode,
		Scale:                 projection.Conversion.Scale,
		FormulaVersion:        projection.Conversion.FormulaVersion,
		ClosedTradingUPnL:     projection.ClosedTradingUPnL.Amount.String(),
		Residual:              newLedgerResidualDTO(projection.Residual),
		Watermark:             newLedgerWatermarkDTO(projection.Watermark),
	}, nil
}

func newPortfolioLedgerDTO(projection ledgerprojection.PortfolioProjection) (portfolioLedgerDTO, error) {
	status := normalizeLedgerVerificationStatus(projection.VerificationStatus)
	if !validLedgerVerificationStatus(status) {
		return portfolioLedgerDTO{}, errors.New("invalid ledger verification status")
	}
	currentOpenUnrealizedPnl := projection.CurrentOpenUnrealizedPnl.Amount.String()
	totalNetPnl := projection.TotalNetPnl.Amount.String()
	return portfolioLedgerDTO{
		PrincipalID:              projection.PrincipalID,
		Mode:                     projection.Mode,
		AsOf:                     ledgerTimeDTO(projection.AsOf),
		ReportingAsset:           projection.ReportingAsset,
		CumulativeRealizedNetPnl: projection.CumulativeRealizedNetPnl.Amount.String(),
		CurrentOpenUnrealizedPnl: &currentOpenUnrealizedPnl,
		TotalNetPnl:              &totalNetPnl,
		ActualFundingCashFlow:    projection.FundingTotal.Amount.String(),
		Fees:                     projection.FeeTotal.Amount.String(),
		OpenPositionCount:        projection.OpenPositionCount,
		ClosedPositionCount:      projection.ClosedPositionCount,
		VerificationStatus:       status,
		ConversionSource:         projection.Conversion.ConversionSource,
		ConversionAsOf:           ledgerTimeDTO(projection.Conversion.ConversionAsOf),
		RoundingMode:             projection.Conversion.RoundingMode,
		Scale:                    projection.Conversion.Scale,
		FormulaVersion:           projection.Conversion.FormulaVersion,
		Residual:                 newLedgerResidualDTO(projection.Residual),
		Watermark:                newLedgerWatermarkDTO(projection.Watermark),
	}, nil
}

func newClosedPositionLineageMetadataFromJSON(payload json.RawMessage) (closedPositionReadmodelMetadata, error) {
	var metadata closedPositionReadmodelMetadata
	if len(payload) == 0 || string(payload) == "null" {
		return metadata, nil
	}
	err := json.Unmarshal(payload, &metadata)
	return metadata, err
}

func newClosedPositionLegDTO(metadata closedPositionLegMetadata) closedPositionLegDTO {
	return closedPositionLegDTO{
		AccountID:        metadata.AccountID,
		Venue:            metadata.Venue,
		Symbol:           metadata.Symbol,
		InstrumentID:     metadata.InstrumentID,
		VenueEnvironment: metadata.VenueEnvironment,
	}
}

func newLedgerScopeDTO(scope ledgerfacts.Scope) ledgerScopeDTO {
	return ledgerScopeDTO{
		PrincipalID:         scope.PrincipalID,
		Mode:                scope.Mode,
		AccountID:           scope.AccountID,
		InstrumentID:        instrumentIDDTO(scope.InstrumentID),
		Start:               ledgerTimeDTO(scope.WindowStart),
		End:                 ledgerTimeDTO(scope.WindowEnd),
		PaginationTraversal: scope.PaginationTraversal,
	}
}

func newLedgerFactsWatermarkDTO(watermark ledgerfacts.SyncWatermark) ledgerFactsWatermarkDTO {
	return ledgerFactsWatermarkDTO{
		Cursor:           watermark.Cursor,
		Revision:         watermark.Revision,
		ObservedAt:       ledgerTimeDTO(watermark.ObservedAt),
		EffectiveThrough: ledgerTimeDTO(watermark.EffectiveThrough),
		CompleteThrough:  ledgerTimeDTO(watermark.CompleteThrough),
		Verified:         watermark.Verified,
		Status:           string(watermark.Status),
	}
}

func newLedgerWatermarkDTO(watermark ledgerprojection.Watermark) ledgerWatermarkDTO {
	return ledgerWatermarkDTO{
		Status:          string(watermark.Status),
		Verified:        watermark.Verified,
		CompleteThrough: ledgerTimeDTO(watermark.CompleteThrough),
	}
}

func newLedgerResidualDTO(residual ledgerprojection.Residual) ledgerResidualDTO {
	return ledgerResidualDTO{
		LegQuantity:            residual.LegQuantity.String(),
		ReportingAssetNotional: residual.ReportingAssetNotional.String(),
		OpenOrderCount:         residual.OpenOrderCount,
		PendingCompensation:    residual.PendingCompensation,
		BlockerClass:           residual.BlockerClass,
	}
}

func ledgerTimeDTO(value time.Time) string {
	if value.IsZero() || value.Year() <= 1 {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func instrumentIDDTO(value string) string {
	if value == ledgerfacts.AllInstruments {
		return ""
	}
	return value
}

func validLedgerVerificationStatus(status string) bool {
	switch normalizeLedgerVerificationStatus(status) {
	case "unsupported", "temporarily_unavailable", "incomplete", "provisional", "verified", "revised", "blocked":
		return true
	default:
		return false
	}
}

func normalizeLedgerVerificationStatus(status string) string {
	return strings.TrimSpace(status)
}
