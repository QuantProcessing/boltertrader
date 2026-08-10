package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/positioneconomics"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

type LedgerFundingStore interface {
	FundingPayments(context.Context, ledgerfacts.FundingPaymentQuery) ([]ledgerfacts.FundingPaymentFact, error)
	SyncWatermarkForFundingQuery(context.Context, ledgerfacts.FundingPaymentQuery) (ledgerfacts.SyncWatermark, bool, error)
}

type LedgerProjectionStore interface {
	ClosedPositions(context.Context, string, string, string) ([]ledgerprojection.ClosedPositionProjection, error)
	ClosedPositionForScope(context.Context, string, string, string, string) (ledgerprojection.ClosedPositionProjection, bool, error)
	Portfolio(context.Context, string, string, string) (ledgerprojection.PortfolioProjection, bool, error)
	Watermark(context.Context, string, string, string) (ledgerprojection.Watermark, bool, error)
}

type LedgerPositionReader interface {
	Position(context.Context, readmodel.Scope, string) (json.RawMessage, error)
}

type LedgerTradingReadiness interface {
	CheckTrading(context.Context) readiness.TradingReadinessResult
}

type LedgerPositionEconomics interface {
	Portfolio(context.Context, readmodel.Scope, string) (positioneconomics.PortfolioSnapshot, error)
}

type LedgerQueryServiceConfig struct {
	Funding           LedgerFundingStore
	Projection        LedgerProjectionStore
	PositionReader    LedgerPositionReader
	PositionEconomics LedgerPositionEconomics
	TradingReadiness  LedgerTradingReadiness
	ReportingAsset    string
	CursorCodec       *readmodel.CursorCodec
}

type LedgerQueryService struct {
	funding           LedgerFundingStore
	projection        LedgerProjectionStore
	positionReader    LedgerPositionReader
	positionEconomics LedgerPositionEconomics
	tradingReadiness  LedgerTradingReadiness
	reportingAsset    string
	cursorCodec       *readmodel.CursorCodec
}

func NewLedgerQueryService(cfg LedgerQueryServiceConfig) *LedgerQueryService {
	asset := strings.TrimSpace(cfg.ReportingAsset)
	if asset == "" {
		asset = "USDT"
	}
	return &LedgerQueryService{
		funding:           cfg.Funding,
		projection:        cfg.Projection,
		positionReader:    cfg.PositionReader,
		positionEconomics: cfg.PositionEconomics,
		tradingReadiness:  cfg.TradingReadiness,
		reportingAsset:    asset,
		cursorCodec:       cfg.CursorCodec,
	}
}

func (s *LedgerQueryService) FundingPayments(ctx context.Context, query ledgerFundingPaymentQuery) (fundingPaymentPageDTO, error) {
	if s == nil || s.funding == nil {
		return fundingPaymentPageDTO{}, errors.New("ledger funding store is required")
	}
	limit := query.Limit
	if limit == 0 {
		limit = 50
	}
	storeQuery := ledgerfacts.FundingPaymentQuery{
		PrincipalID:  strings.TrimSpace(query.PrincipalID),
		Mode:         strings.TrimSpace(query.Mode),
		AccountID:    strings.TrimSpace(query.AccountID),
		InstrumentID: strings.TrimSpace(query.InstrumentID),
		PositionID:   strings.TrimSpace(query.PositionID),
		From:         query.From,
		To:           query.To,
		Limit:        limit + 1,
	}
	expectation := readmodel.CursorExpectation{
		Resource: readmodel.FundingPayments, PrincipalID: storeQuery.PrincipalID,
		Mode: storeQuery.Mode, ContractVersion: readmodel.ContractVersion,
		Fingerprint: fundingPaymentFingerprint(storeQuery),
	}
	if strings.TrimSpace(query.Cursor) != "" {
		if s.cursorCodec == nil {
			return fundingPaymentPageDTO{}, readmodel.ErrInvalidCursor
		}
		position, err := s.cursorCodec.Decode(query.Cursor, expectation)
		if err != nil {
			return fundingPaymentPageDTO{}, err
		}
		storeQuery.CursorEffectiveAt, err = time.Parse(time.RFC3339Nano, position.SortValue)
		if err != nil {
			return fundingPaymentPageDTO{}, readmodel.ErrInvalidCursor
		}
		storeQuery.CursorSequenceID, err = strconv.ParseInt(position.ID, 10, 64)
		if err != nil || storeQuery.CursorSequenceID <= 0 {
			return fundingPaymentPageDTO{}, readmodel.ErrInvalidCursor
		}
	}
	items, err := s.funding.FundingPayments(ctx, storeQuery)
	if err != nil {
		return fundingPaymentPageDTO{}, err
	}
	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		if s.cursorCodec == nil || last.SequenceID <= 0 || last.Provenance.EffectiveAt.IsZero() {
			return fundingPaymentPageDTO{}, readmodel.ErrInvalidCursor
		}
		nextCursor, err = s.cursorCodec.Encode(readmodel.CursorPosition{
			Resource: readmodel.FundingPayments, PrincipalID: storeQuery.PrincipalID,
			Mode: storeQuery.Mode, ContractVersion: readmodel.ContractVersion,
			Fingerprint: expectation.Fingerprint,
			SortValue:   last.Provenance.EffectiveAt.UTC().Format(time.RFC3339Nano),
			ID:          strconv.FormatInt(last.SequenceID, 10), OccurredAt: last.Provenance.ObservedAt,
		})
		if err != nil {
			return fundingPaymentPageDTO{}, err
		}
	}
	scope := ledgerfacts.Scope{
		PrincipalID:  storeQuery.PrincipalID,
		Mode:         storeQuery.Mode,
		AccountID:    storeQuery.AccountID,
		InstrumentID: storeQuery.InstrumentID,
		WindowStart:  query.From,
		WindowEnd:    query.To,
	}
	page := fundingPaymentPage{
		Items:      items,
		NextCursor: nextCursor,
		Scope:      scope,
		Status:     fundingPageStatus(items, ledgerfacts.SyncWatermark{}, false),
		AsOf:       fundingPageAsOf(items),
	}
	if !storeQuery.From.IsZero() && !storeQuery.To.IsZero() {
		watermarkQuery := storeQuery
		watermarkQuery.Limit = 0
		watermarkQuery.CursorEffectiveAt = time.Time{}
		watermarkQuery.CursorSequenceID = 0
		watermark, ok, err := s.funding.SyncWatermarkForFundingQuery(ctx, watermarkQuery)
		if err != nil {
			return fundingPaymentPageDTO{}, err
		}
		if ok {
			if !watermarkMatchesFundingQuery(watermark, watermarkQuery) {
				return fundingPaymentPageDTO{}, ledgerfacts.ErrScopeMismatch
			}
			page.Scope = watermark.Scope
			page.Watermark = watermark
			page.Status = fundingPageStatus(items, watermark, true)
			page.AsOf = watermark.ObservedAt
			page.CompleteThrough = watermark.CompleteThrough
			page.Revision = watermark.Revision
		}
	}
	return newFundingPaymentPageDTO(page), nil
}

func (s *LedgerQueryService) Portfolio(ctx context.Context, scope readmodel.Scope, asset string) (portfolioLedgerDTO, bool, error) {
	if s == nil || s.projection == nil {
		return portfolioLedgerDTO{}, false, errors.New("ledger projection store is required")
	}
	asset = reportingAsset(asset, s.reportingAsset)
	projection, ok, err := s.projection.Portfolio(ctx, strings.TrimSpace(scope.PrincipalID), strings.TrimSpace(scope.Mode), asset)
	if err != nil || !ok {
		return portfolioLedgerDTO{}, ok, err
	}
	dto, err := newPortfolioLedgerDTO(projection)
	if err != nil {
		return portfolioLedgerDTO{}, false, err
	}
	if s.positionEconomics == nil {
		return dto, true, nil
	}
	live, err := s.positionEconomics.Portfolio(ctx, scope, asset)
	if err != nil {
		return portfolioLedgerDTO{}, false, err
	}
	dto.AsOf = ledgerTimeDTO(live.AsOf)
	dto.OpenPositionCount = live.OpenPositionCount
	if !live.Available {
		dto.CurrentOpenUnrealizedPnl = nil
		dto.TotalNetPnl = nil
		dto.LiveEconomicsStatus = "incomplete"
		return dto, true, nil
	}
	current := live.UnrealizedPnl.String()
	total := projection.CumulativeRealizedNetPnl.Amount.Add(live.UnrealizedPnl).String()
	dto.CurrentOpenUnrealizedPnl = &current
	dto.TotalNetPnl = &total
	dto.LiveEconomicsStatus = "available"
	return dto, true, nil
}

func (s *LedgerQueryService) ClosedPositions(ctx context.Context, scope readmodel.Scope, asset string) ([]closedPositionLedgerDTO, error) {
	if s == nil || s.projection == nil {
		return nil, errors.New("ledger projection store is required")
	}
	items, err := s.projection.ClosedPositions(ctx, strings.TrimSpace(scope.PrincipalID), strings.TrimSpace(scope.Mode), reportingAsset(asset, s.reportingAsset))
	if err != nil {
		return nil, err
	}
	out := make([]closedPositionLedgerDTO, 0, len(items))
	for _, item := range items {
		dto, err := s.closedPositionDTO(ctx, scope, item)
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *LedgerQueryService) ClosedPosition(ctx context.Context, scope readmodel.Scope, positionID string) (closedPositionLedgerDTO, bool, error) {
	if s == nil || s.projection == nil {
		return closedPositionLedgerDTO{}, false, errors.New("ledger projection store is required")
	}
	item, ok, err := s.projection.ClosedPositionForScope(
		ctx,
		strings.TrimSpace(scope.PrincipalID),
		strings.TrimSpace(scope.Mode),
		s.reportingAsset,
		strings.TrimSpace(positionID),
	)
	if err != nil || !ok {
		return closedPositionLedgerDTO{}, ok, err
	}
	dto, err := s.closedPositionDTO(ctx, scope, item)
	if err != nil {
		return closedPositionLedgerDTO{}, false, err
	}
	return dto, true, nil
}

func (s *LedgerQueryService) Verification(ctx context.Context, scope readmodel.Scope) (verificationStatusDTO, error) {
	if s == nil || s.projection == nil {
		return verificationStatusDTO{}, errors.New("ledger projection store is required")
	}
	watermark, ok, err := s.projection.Watermark(ctx, strings.TrimSpace(scope.PrincipalID), strings.TrimSpace(scope.Mode), s.reportingAsset)
	if err != nil {
		return verificationStatusDTO{}, err
	}
	trading := readiness.TradingReadinessResult{Reason: readiness.TradingReadinessReasonNotReady}
	if s.tradingReadiness != nil {
		trading = s.tradingReadiness.CheckTrading(ctx)
	}
	if !ok {
		return newVerificationStatusDTO("temporarily_unavailable", "ledger_projection_unavailable", "", ledgerprojection.Watermark{}, trading)
	}
	return newVerificationStatusDTO(verificationStatusFromWatermark(watermark), string(trading.Reason), "", watermark, trading)
}

func (s *LedgerQueryService) closedPositionDTO(ctx context.Context, scope readmodel.Scope, item ledgerprojection.ClosedPositionProjection) (closedPositionLedgerDTO, error) {
	metadata, err := s.closedPositionMetadata(ctx, scope, item.PositionID)
	if err != nil {
		return closedPositionLedgerDTO{}, err
	}
	return newClosedPositionLedgerDTO(item, metadata)
}

func (s *LedgerQueryService) closedPositionMetadata(ctx context.Context, scope readmodel.Scope, positionID string) (closedPositionReadmodelMetadata, error) {
	if s == nil || s.positionReader == nil {
		return closedPositionReadmodelMetadata{}, nil
	}
	payload, err := s.positionReader.Position(ctx, scope, positionID)
	if errors.Is(err, readmodel.ErrNotFound) {
		return closedPositionReadmodelMetadata{}, nil
	}
	if err != nil {
		return closedPositionReadmodelMetadata{}, err
	}
	return newClosedPositionLineageMetadataFromJSON(payload)
}

func reportingAsset(requested, fallback string) string {
	asset := strings.TrimSpace(requested)
	if asset != "" {
		return asset
	}
	if fallback != "" {
		return fallback
	}
	return "USDT"
}

func fundingPageStatus(items []ledgerfacts.FundingPaymentFact, watermark ledgerfacts.SyncWatermark, hasWatermark bool) ledgerfacts.CompletenessStatus {
	if hasWatermark && watermark.Status != "" {
		return watermark.Status
	}
	return ledgerfacts.Incomplete
}

func fundingPaymentFingerprint(query ledgerfacts.FundingPaymentQuery) string {
	return strings.Join([]string{
		query.AccountID,
		query.InstrumentID,
		query.PositionID,
		query.From.UTC().Format(time.RFC3339Nano),
		query.To.UTC().Format(time.RFC3339Nano),
	}, "|")
}

func watermarkMatchesFundingQuery(watermark ledgerfacts.SyncWatermark, query ledgerfacts.FundingPaymentQuery) bool {
	if watermark.Scope.PrincipalID != query.PrincipalID || watermark.Scope.Mode != query.Mode ||
		watermark.Scope.AccountID != query.AccountID || watermark.Scope.InstrumentID != query.InstrumentID {
		return false
	}
	return !query.From.IsZero() && !query.To.IsZero() &&
		watermark.Scope.WindowStart.Equal(query.From) &&
		watermark.Scope.WindowEnd.Equal(query.To)
}

func fundingPageAsOf(items []ledgerfacts.FundingPaymentFact) time.Time {
	var asOf time.Time
	for _, item := range items {
		if item.Provenance.ObservedAt.After(asOf) {
			asOf = item.Provenance.ObservedAt
		}
	}
	return asOf
}

func verificationStatusFromWatermark(watermark ledgerprojection.Watermark) string {
	if watermark.Verified {
		return "verified"
	}
	switch watermark.Status {
	case ledgerfacts.Unsupported:
		return "unsupported"
	case ledgerfacts.TemporarilyUnavailable:
		return "temporarily_unavailable"
	case ledgerfacts.Incomplete:
		return "incomplete"
	default:
		return "provisional"
	}
}
