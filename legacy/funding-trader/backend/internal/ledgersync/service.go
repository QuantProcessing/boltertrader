package ledgersync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/shopspring/decimal"
)

const (
	defaultPageLimit = 100
	defaultMaxPages  = 20

	fundingPaymentTopic = "ledger.funding_payment.append"
	verificationTopic   = "verification.status"
	sourceRef           = "gateway:funding-payments"
)

var (
	ErrInvalidTarget        = errors.New("ledger sync target is invalid")
	ErrPaginationCycle      = errors.New("ledger sync pagination cursor cycle")
	ErrPaginationLimit      = errors.New("ledger sync pagination page limit exceeded")
	ErrScopeDrift           = errors.New("ledger sync gateway scope drift")
	ErrInvalidUpstream      = errors.New("ledger sync upstream funding payment is invalid")
	ErrSupersededUnresolved = errors.New("ledger sync superseded funding fact is unresolved")
)

type Gateway interface {
	FundingPayments(context.Context, boltertrader.GatewayFundingPaymentRequest) (boltertrader.FundingPaymentPageResource, error)
}

type Sink interface {
	IngestFundingPayments(context.Context, ledgerfacts.FundingIngestBatch) ([]ledgerfacts.FundingPaymentFact, ledgerfacts.SyncWatermark, error)
	AppendSyncWatermark(context.Context, ledgerfacts.SyncWatermark) (ledgerfacts.SyncWatermark, error)
}

type Publisher interface {
	Publish(context.Context, Event) error
}

type Resolver interface {
	ResolveGatewayFundingFactID(context.Context, ledgerfacts.Scope, string) (string, bool, error)
}

type Event struct {
	Topic   string
	Payload map[string]any
}

type ServiceOptions struct {
	Publisher Publisher
	Resolver  Resolver
	PageLimit int
	MaxPages  int
}

type Service struct {
	gateway   Gateway
	sink      Sink
	publisher Publisher
	resolver  Resolver
	pageLimit int
	maxPages  int
}

type SyncTarget struct {
	PrincipalID     string
	Mode            string
	AccountID       string
	InstrumentID    string
	SettlementAsset string
	WindowStart     time.Time
	WindowEnd       time.Time
	PositionID      string
}

type SyncResult struct {
	Status    ledgerfacts.CompletenessStatus
	Verified  bool
	Payments  int
	Watermark ledgerfacts.SyncWatermark
}

func NewService(gateway Gateway, sink Sink, options ServiceOptions) *Service {
	pageLimit := options.PageLimit
	if pageLimit <= 0 {
		pageLimit = defaultPageLimit
	}
	maxPages := options.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	return &Service{
		gateway: gateway, sink: sink, publisher: options.Publisher, resolver: options.Resolver,
		pageLimit: pageLimit, maxPages: maxPages,
	}
}

func (service *Service) SyncTarget(ctx context.Context, target SyncTarget) (SyncResult, error) {
	if err := validateTarget(target); err != nil {
		return SyncResult{}, err
	}
	if service == nil || service.gateway == nil || service.sink == nil {
		return SyncResult{}, fmt.Errorf("%w: service dependencies", ErrInvalidTarget)
	}

	scope := target.ledgerScope("")
	cursor := ""
	seen := map[string]struct{}{}
	type scopedResource struct {
		page boltertrader.FundingPaymentPageResource
		item boltertrader.FundingPaymentResource
	}
	var resources []scopedResource
	traversalCursors := []string{""}
	var lastPage boltertrader.FundingPaymentPageResource

	for pageNumber := 0; ; pageNumber++ {
		if pageNumber >= service.maxPages {
			return SyncResult{}, ErrPaginationLimit
		}
		if cursor != "" {
			if _, ok := seen[cursor]; ok {
				return SyncResult{}, ErrPaginationCycle
			}
			seen[cursor] = struct{}{}
		}
		page, err := service.gateway.FundingPayments(ctx, boltertrader.GatewayFundingPaymentRequest{
			AccountID:       target.AccountID,
			InstrumentID:    target.InstrumentID,
			SettlementAsset: target.SettlementAsset,
			Start:           target.WindowStart,
			End:             target.WindowEnd,
			Cursor:          cursor,
			Limit:           service.pageLimit,
		})
		if err != nil {
			return service.commitUnavailableWatermark(ctx, target, err)
		}
		if err := validatePageScope(target, page); err != nil {
			return SyncResult{}, err
		}
		pageScope := target.ledgerScope(page.Scope.InstrumentID)
		if page.Scope.AllInstruments || page.Scope.InstrumentID == "" {
			pageScope.InstrumentID = ledgerfacts.AllInstruments
		}
		if pageNumber == 0 {
			scope = pageScope
		} else if !sameScope(scope, pageScope) {
			return SyncResult{}, ErrScopeDrift
		}
		for _, item := range page.Items {
			if err := validatePaymentResource(target, page, item); err != nil {
				return SyncResult{}, err
			}
			resources = append(resources, scopedResource{page: page, item: item})
		}
		lastPage = page

		nextCursor := strings.TrimSpace(page.NextCursor)
		if nextCursor == "" {
			nextCursor = strings.TrimSpace(page.Watermark.Cursor)
		}
		hasMore := page.PaginationTraversal.HasMore || page.Page.HasMore || nextCursor != ""
		if !hasMore {
			break
		}
		if nextCursor == "" {
			return SyncResult{}, fmt.Errorf("%w: missing next cursor", ErrInvalidUpstream)
		}
		if nextCursor == cursor {
			return SyncResult{}, ErrPaginationCycle
		}
		cursor = nextCursor
		traversalCursors = append(traversalCursors, cursor)
	}

	scope.PaginationTraversal = paginationTraversal(service.pageLimit, traversalCursors)
	status := ledgerfacts.CompletenessStatus(lastPage.Status)
	if status == ledgerfacts.CompleteEmpty && len(resources) > 0 {
		status = ledgerfacts.Complete
	}
	watermark := watermarkFromPage(scope, lastPage, status)
	if !isFinal(watermark.Status) {
		committed, err := service.sink.AppendSyncWatermark(ctx, watermark)
		if err != nil {
			return SyncResult{Status: watermark.Status, Watermark: watermark}, err
		}
		service.publishVerification(ctx, committed, 0, nil)
		return SyncResult{Status: committed.Status, Verified: committed.Verified, Watermark: committed}, nil
	}
	payments := make([]ledgerfacts.FundingPaymentFact, 0, len(resources))
	for _, resource := range resources {
		mapped, err := service.mapPayment(ctx, target, scope, resource.page, resource.item)
		if err != nil {
			return SyncResult{}, err
		}
		payments = append(payments, mapped)
	}

	batch := ledgerfacts.FundingIngestBatch{Scope: scope, Payments: payments, Watermark: watermark}
	committedPayments, committedWatermark, err := service.sink.IngestFundingPayments(ctx, batch)
	if err != nil {
		return SyncResult{Status: watermark.Status, Watermark: watermark}, err
	}
	for _, payment := range committedPayments {
		service.publishPayment(ctx, payment)
	}
	service.publishVerification(ctx, committedWatermark, len(committedPayments), nil)
	return SyncResult{
		Status:    committedWatermark.Status,
		Verified:  committedWatermark.Verified,
		Payments:  len(committedPayments),
		Watermark: committedWatermark,
	}, nil
}

func (service *Service) commitUnavailableWatermark(ctx context.Context, target SyncTarget, upstreamErr error) (SyncResult, error) {
	scope := target.ledgerScope(target.InstrumentID)
	scope.PaginationTraversal = "gateway_funding_payments:error"
	watermark := ledgerfacts.SyncWatermark{
		Scope:     scope,
		SourceRef: sourceRef,
		Revision:  "gateway-error",
		Status:    ledgerfacts.TemporarilyUnavailable,
		Verified:  false,
	}
	committed, err := service.sink.AppendSyncWatermark(ctx, watermark)
	if err != nil {
		return SyncResult{Status: watermark.Status, Watermark: watermark}, err
	}
	service.publishVerification(ctx, committed, 0, map[string]any{"error_class": "gateway_unavailable"})
	return SyncResult{Status: committed.Status, Verified: false, Watermark: committed}, sanitizedGatewayError(upstreamErr)
}

func (service *Service) mapPayment(ctx context.Context, target SyncTarget, scope ledgerfacts.Scope, page boltertrader.FundingPaymentPageResource, item boltertrader.FundingPaymentResource) (ledgerfacts.FundingPaymentFact, error) {
	amount, err := decimal.NewFromString(item.Amount)
	if err != nil {
		return ledgerfacts.FundingPaymentFact{}, fmt.Errorf("%w: amount", ErrInvalidUpstream)
	}
	supersedes, err := service.resolveSupersedes(ctx, scope, item.SupersedesFactID)
	if err != nil {
		return ledgerfacts.FundingPaymentFact{}, err
	}
	revision := revisionString(item.Revision, page.Revision, page.Watermark.Revision)
	sourceReference := strings.TrimSpace(item.FactID)
	if sourceReference == "" {
		sourceReference = strings.TrimSpace(item.SourceRef)
	}
	if sourceReference == "" {
		sourceReference = strings.TrimSpace(item.PaymentID)
	}
	if sourceReference == "" {
		return ledgerfacts.FundingPaymentFact{}, fmt.Errorf("%w: source ref", ErrInvalidUpstream)
	}
	observedAt := item.ObservedAt
	if observedAt.IsZero() {
		observedAt = page.Watermark.ObservedAt
	}
	effectiveAt := item.EffectiveAt
	if effectiveAt.IsZero() {
		effectiveAt = page.Watermark.EffectiveThrough
	}
	return ledgerfacts.FundingPaymentFact{
		SupersedesFactID: supersedes,
		PaymentID:        item.PaymentID,
		PositionID:       target.PositionID,
		Venue:            string(item.Venue),
		VenueEnvironment: item.VenueEnvironment,
		Product:          string(item.Product),
		FormulaVersion:   item.FormulaVersion,
		Scope:            scope,
		Provenance: ledgerfacts.Provenance{
			SourceRef:      sourceReference,
			SourceRevision: revision,
			SourceVersion:  item.Version,
			ObservedAt:     observedAt,
			EffectiveAt:    effectiveAt,
		},
		Payment: ledgerfacts.Money{Asset: item.SettlementAsset, Amount: amount},
	}, nil
}

func (service *Service) resolveSupersedes(ctx context.Context, scope ledgerfacts.Scope, gatewayFactID string) (string, error) {
	gatewayFactID = strings.TrimSpace(gatewayFactID)
	if gatewayFactID == "" {
		return "", nil
	}
	if service.resolver == nil {
		return "", fmt.Errorf("%w: %s", ErrSupersededUnresolved, gatewayFactID)
	}
	factID, ok, err := service.resolver.ResolveGatewayFundingFactID(ctx, scope, gatewayFactID)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(factID) == "" {
		return "", fmt.Errorf("%w: %s", ErrSupersededUnresolved, gatewayFactID)
	}
	return factID, nil
}

func validateTarget(target SyncTarget) error {
	if strings.TrimSpace(target.PrincipalID) == "" ||
		(target.Mode != "test" && target.Mode != "live") ||
		strings.TrimSpace(target.AccountID) == "" ||
		strings.TrimSpace(target.SettlementAsset) == "" ||
		target.WindowStart.IsZero() ||
		target.WindowEnd.IsZero() ||
		!target.WindowEnd.After(target.WindowStart) {
		return ErrInvalidTarget
	}
	return nil
}

func validatePageScope(target SyncTarget, page boltertrader.FundingPaymentPageResource) error {
	if page.Scope.AccountID != "" && page.Scope.AccountID != target.AccountID {
		return ErrScopeDrift
	}
	if page.AccountID != "" && page.AccountID != target.AccountID {
		return ErrScopeDrift
	}
	if page.Scope.SettlementAsset != "" && page.Scope.SettlementAsset != target.SettlementAsset {
		return ErrScopeDrift
	}
	if page.SettlementAsset != "" && page.SettlementAsset != target.SettlementAsset {
		return ErrScopeDrift
	}
	if !page.Scope.AllInstruments && target.InstrumentID != "" && page.Scope.InstrumentID != "" && page.Scope.InstrumentID != target.InstrumentID {
		return ErrScopeDrift
	}
	if !page.Scope.AllInstruments && target.InstrumentID != "" && page.InstrumentID != "" && page.InstrumentID != target.InstrumentID {
		return ErrScopeDrift
	}
	if !sameWindow(page.Scope.Start, target.WindowStart) || !sameWindow(page.Scope.End, target.WindowEnd) {
		return ErrScopeDrift
	}
	if !page.Start.IsZero() && !sameWindow(page.Start, target.WindowStart) {
		return ErrScopeDrift
	}
	if !page.End.IsZero() && !sameWindow(page.End, target.WindowEnd) {
		return ErrScopeDrift
	}
	if !validStatus(page.Status) {
		return fmt.Errorf("%w: status", ErrInvalidUpstream)
	}
	return nil
}

func validatePaymentResource(target SyncTarget, page boltertrader.FundingPaymentPageResource, item boltertrader.FundingPaymentResource) error {
	if strings.TrimSpace(item.PaymentID) == "" ||
		strings.TrimSpace(item.SettlementAsset) == "" ||
		strings.TrimSpace(item.Amount) == "" ||
		strings.TrimSpace(item.VenueEnvironment) == "" ||
		strings.TrimSpace(string(item.Product)) == "" {
		return ErrInvalidUpstream
	}
	if item.AccountID != "" && item.AccountID != target.AccountID {
		return ErrScopeDrift
	}
	if item.SettlementAsset != target.SettlementAsset {
		return ErrScopeDrift
	}
	if !page.Scope.AllInstruments && target.InstrumentID != "" && item.InstrumentID != "" && item.InstrumentID != target.InstrumentID {
		return ErrScopeDrift
	}
	return nil
}

func watermarkFromPage(scope ledgerfacts.Scope, page boltertrader.FundingPaymentPageResource, status ledgerfacts.CompletenessStatus) ledgerfacts.SyncWatermark {
	revision := revisionString(page.Watermark.Revision, page.Revision)
	observedAt := page.Watermark.ObservedAt
	if observedAt.IsZero() {
		observedAt = page.AsOf
	}
	effectiveThrough := page.Watermark.EffectiveThrough
	if effectiveThrough.IsZero() {
		effectiveThrough = page.CompleteThrough
	}
	completeThrough := page.Watermark.CompleteThrough
	if completeThrough.IsZero() {
		completeThrough = page.CompleteThrough
	}
	return ledgerfacts.SyncWatermark{
		Scope:            scope,
		SourceRef:        sourceRef,
		Cursor:           page.Watermark.Cursor,
		Revision:         revision,
		ObservedAt:       observedAt,
		EffectiveThrough: effectiveThrough,
		CompleteThrough:  completeThrough,
		Status:           status,
		Verified:         isFinal(status) && page.Watermark.Verified,
	}
}

func (target SyncTarget) ledgerScope(instrumentID string) ledgerfacts.Scope {
	instrumentID = strings.TrimSpace(instrumentID)
	if instrumentID == "" {
		instrumentID = strings.TrimSpace(target.InstrumentID)
	}
	if instrumentID == "" {
		instrumentID = ledgerfacts.AllInstruments
	}
	return ledgerfacts.Scope{
		PrincipalID:         target.PrincipalID,
		Mode:                target.Mode,
		AccountID:           target.AccountID,
		InstrumentID:        instrumentID,
		WindowStart:         target.WindowStart.UTC(),
		WindowEnd:           target.WindowEnd.UTC(),
		PaginationTraversal: "gateway_funding_payments:pending",
	}
}

func paginationTraversal(limit int, cursors []string) string {
	digest := sha256.Sum256([]byte(strings.Join(cursors, "\x00")))
	return fmt.Sprintf("gateway_funding_payments:v1:limit=%d:pages=%d:sha256=%s", limit, len(cursors), hex.EncodeToString(digest[:16]))
}

func (service *Service) publishPayment(ctx context.Context, payment ledgerfacts.FundingPaymentFact) {
	if service.publisher == nil {
		return
	}
	_ = service.publisher.Publish(ctx, Event{
		Topic: fundingPaymentTopic,
		Payload: map[string]any{
			"factId":           payment.FactID,
			"paymentId":        payment.PaymentID,
			"supersedesFactId": payment.SupersedesFactID,
			"syncWatermarkId":  payment.SyncWatermarkID,
			"accountId":        payment.Scope.AccountID,
			"instrumentId":     payment.Scope.InstrumentID,
			"amount":           payment.Payment.Amount.String(),
			"asset":            payment.Payment.Asset,
			"sourceRef":        payment.Provenance.SourceRef,
			"sourceRevision":   payment.Provenance.SourceRevision,
		},
	})
}

func (service *Service) publishVerification(ctx context.Context, watermark ledgerfacts.SyncWatermark, paymentCount int, extra map[string]any) {
	if service.publisher == nil {
		return
	}
	payload := map[string]any{
		"watermarkId":  watermark.FactID,
		"status":       string(watermark.Status),
		"verified":     watermark.Verified,
		"paymentCount": paymentCount,
		"accountId":    watermark.Scope.AccountID,
		"instrumentId": watermark.Scope.InstrumentID,
		"sourceRef":    watermark.SourceRef,
		"revision":     watermark.Revision,
	}
	for key, value := range extra {
		payload[key] = value
	}
	_ = service.publisher.Publish(ctx, Event{Topic: verificationTopic, Payload: payload})
}

func validStatus(status exchange.FundingPaymentStatus) bool {
	switch status {
	case exchange.FundingPaymentsUnsupported,
		exchange.FundingPaymentsTemporarilyUnavailable,
		exchange.FundingPaymentsIncomplete,
		exchange.FundingPaymentsCompleteEmpty,
		exchange.FundingPaymentsComplete:
		return true
	default:
		return false
	}
}

func isFinal(status ledgerfacts.CompletenessStatus) bool {
	return status == ledgerfacts.Complete || status == ledgerfacts.CompleteEmpty
}

func sameScope(left, right ledgerfacts.Scope) bool {
	return left.PrincipalID == right.PrincipalID &&
		left.Mode == right.Mode &&
		left.AccountID == right.AccountID &&
		left.InstrumentID == right.InstrumentID &&
		left.WindowStart.Equal(right.WindowStart) &&
		left.WindowEnd.Equal(right.WindowEnd) &&
		left.PaginationTraversal == right.PaginationTraversal
}

func sameWindow(left, right time.Time) bool {
	return left.UTC().Equal(right.UTC())
}

func revisionString(values ...int64) string {
	for _, value := range values {
		if value != 0 {
			return strconv.FormatInt(value, 10)
		}
	}
	return "0"
}

func sanitizedGatewayError(error) error {
	return errors.New("gateway funding payments temporarily unavailable")
}
