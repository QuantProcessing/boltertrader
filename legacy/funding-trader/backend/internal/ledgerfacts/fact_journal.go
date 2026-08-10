package ledgerfacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Journal struct {
	store journalStore
}

type journalStore interface {
	ingestFundingPayments(context.Context, FundingIngestBatch) ([]FundingPaymentFact, SyncWatermark, error)
	appendFunding(context.Context, FundingPaymentFact, string) (FundingPaymentFact, bool, error)
	getFunding(context.Context, string) (FundingPaymentFact, string, bool, error)
	appendFill(context.Context, FillCashflowFact, string) (FillCashflowFact, bool, error)
	getFill(context.Context, string) (FillCashflowFact, string, bool, error)
	appendPosition(context.Context, PositionSnapshotFact, string) (PositionSnapshotFact, bool, error)
	getPosition(context.Context, string) (PositionSnapshotFact, string, bool, error)
	appendWatermark(context.Context, SyncWatermark, string) (SyncWatermark, bool, error)
	acquireLineage(context.Context, LineageFact, string) (LineageFact, bool, error)
	getLineage(context.Context, string) (LineageFact, string, bool, error)
	appendLineageRevision(context.Context, LineageRevisionFact, string) (LineageRevisionFact, bool, error)
	getLineageRevision(context.Context, string) (LineageRevisionFact, string, bool, error)
	releaseLineageWithRevision(context.Context, string, LineageRevisionFact, string, time.Time) (LineageRevisionFact, error)
	replay(context.Context, Scope) ([]ReplayEntry, error)
	portfolioScopes(context.Context, string, string, string) ([]Scope, error)
}

func NewJournal(store journalStore) *Journal {
	return &Journal{store: store}
}

func (j *Journal) AppendFundingPayment(ctx context.Context, fact FundingPaymentFact) (FundingPaymentFact, error) {
	return j.appendFundingPayment(ctx, fact, false)
}

func (j *Journal) appendFundingPayment(ctx context.Context, fact FundingPaymentFact, allowSupersession bool) (FundingPaymentFact, error) {
	if j == nil || j.store == nil {
		return FundingPaymentFact{}, fmt.Errorf("ledger journal store is required")
	}
	if fact.SupersedesFactID != "" && !allowSupersession {
		return FundingPaymentFact{}, ErrFactConflict
	}
	if err := validateScope(fact.Scope); err != nil {
		return FundingPaymentFact{}, err
	}
	if strings.TrimSpace(fact.Payment.Asset) == "" {
		return FundingPaymentFact{}, ErrCompletenessViolation
	}
	fact.FactID = stableID("funding", fact.Scope, fact.Provenance.SourceRef, fact.Provenance.SourceRevision)
	hash := payloadHash(fact)
	stored, existed, err := j.store.appendFunding(ctx, fact, hash)
	if err != nil {
		return FundingPaymentFact{}, err
	}
	if existed && !sameFundingPaymentPayload(stored, fact) {
		return FundingPaymentFact{}, ErrFactConflict
	}
	return stored, nil
}

func (j *Journal) AppendSupersedingFundingPayment(ctx context.Context, supersedesFactID string, fact FundingPaymentFact) (FundingPaymentFact, error) {
	target, _, ok, err := j.store.getFunding(ctx, supersedesFactID)
	if err != nil {
		return FundingPaymentFact{}, err
	}
	if !ok {
		return FundingPaymentFact{}, ErrFactNotFound
	}
	if !sameExactScope(target.Scope, fact.Scope) {
		return FundingPaymentFact{}, ErrScopeMismatch
	}
	if compareRevision(fact.Provenance.SourceRevision, target.Provenance.SourceRevision) <= 0 {
		return FundingPaymentFact{}, ErrRevisionOrder
	}
	fact.SupersedesFactID = supersedesFactID
	return j.appendFundingPayment(ctx, fact, true)
}

func (j *Journal) AppendFillCashflow(ctx context.Context, fact FillCashflowFact) (FillCashflowFact, error) {
	return j.appendFillCashflow(ctx, fact, false)
}

func (j *Journal) appendFillCashflow(ctx context.Context, fact FillCashflowFact, allowSupersession bool) (FillCashflowFact, error) {
	if j == nil || j.store == nil {
		return FillCashflowFact{}, fmt.Errorf("ledger journal store is required")
	}
	if fact.SupersedesFactID != "" && !allowSupersession {
		return FillCashflowFact{}, ErrFactConflict
	}
	if err := validateScope(fact.Scope); err != nil {
		return FillCashflowFact{}, err
	}
	fact.FactID = stableID("fill_cashflow", fact.Scope, fact.FillID+"|"+fact.Provenance.SourceRef, fact.Provenance.SourceRevision)
	hash := payloadHash(fact)
	stored, existed, err := j.store.appendFill(ctx, fact, hash)
	if err != nil {
		return FillCashflowFact{}, err
	}
	if existed && payloadHash(stored) != hash {
		return FillCashflowFact{}, ErrFactConflict
	}
	return stored, nil
}

func (j *Journal) AppendSupersedingFillCashflow(ctx context.Context, supersedesFactID string, fact FillCashflowFact) (FillCashflowFact, error) {
	target, _, ok, err := j.store.getFill(ctx, supersedesFactID)
	if err != nil {
		return FillCashflowFact{}, err
	}
	if !ok {
		return FillCashflowFact{}, ErrFactNotFound
	}
	if !sameExactScope(target.Scope, fact.Scope) {
		return FillCashflowFact{}, ErrScopeMismatch
	}
	if compareRevision(fact.Provenance.SourceRevision, target.Provenance.SourceRevision) <= 0 {
		return FillCashflowFact{}, ErrRevisionOrder
	}
	fact.SupersedesFactID = supersedesFactID
	return j.appendFillCashflow(ctx, fact, true)
}

func (j *Journal) AppendPositionSnapshot(ctx context.Context, fact PositionSnapshotFact) (PositionSnapshotFact, error) {
	return j.appendPositionSnapshot(ctx, fact, false)
}

func (j *Journal) appendPositionSnapshot(ctx context.Context, fact PositionSnapshotFact, allowSupersession bool) (PositionSnapshotFact, error) {
	if j == nil || j.store == nil {
		return PositionSnapshotFact{}, fmt.Errorf("ledger journal store is required")
	}
	if fact.SupersedesFactID != "" && !allowSupersession {
		return PositionSnapshotFact{}, ErrFactConflict
	}
	if err := validateScope(fact.Scope); err != nil {
		return PositionSnapshotFact{}, err
	}
	fact.FactID = stableID("position_snapshot", fact.Scope, fact.PositionID+"|"+fact.Provenance.SourceRef, fact.Provenance.SourceRevision)
	hash := payloadHash(fact)
	stored, existed, err := j.store.appendPosition(ctx, fact, hash)
	if err != nil {
		return PositionSnapshotFact{}, err
	}
	if existed && payloadHash(stored) != hash {
		return PositionSnapshotFact{}, ErrFactConflict
	}
	return stored, nil
}

func (j *Journal) AppendSupersedingPositionSnapshot(ctx context.Context, supersedesFactID string, fact PositionSnapshotFact) (PositionSnapshotFact, error) {
	target, _, ok, err := j.store.getPosition(ctx, supersedesFactID)
	if err != nil {
		return PositionSnapshotFact{}, err
	}
	if !ok {
		return PositionSnapshotFact{}, ErrFactNotFound
	}
	if !sameExactScope(target.Scope, fact.Scope) {
		return PositionSnapshotFact{}, ErrScopeMismatch
	}
	if compareRevision(fact.Provenance.SourceRevision, target.Provenance.SourceRevision) <= 0 {
		return PositionSnapshotFact{}, ErrRevisionOrder
	}
	fact.SupersedesFactID = supersedesFactID
	return j.appendPositionSnapshot(ctx, fact, true)
}

func (j *Journal) AppendSyncWatermark(ctx context.Context, watermark SyncWatermark) (SyncWatermark, error) {
	if j == nil || j.store == nil {
		return SyncWatermark{}, fmt.Errorf("ledger journal store is required")
	}
	if err := validateScope(watermark.Scope); err != nil {
		return SyncWatermark{}, err
	}
	if !validStatus(watermark.Status) || watermark.Verified {
		return SyncWatermark{}, ErrCompletenessViolation
	}
	watermark.FactID = stableID("watermark", watermark.Scope, watermark.SourceRef, watermark.Revision+"|"+watermark.Cursor)
	hash := payloadHash(watermark)
	stored, existed, err := j.store.appendWatermark(ctx, watermark, hash)
	if err != nil {
		return SyncWatermark{}, err
	}
	if existed && !sameSyncWatermarkPayload(stored, watermark) {
		return SyncWatermark{}, ErrFactConflict
	}
	return stored, nil
}

func (j *Journal) IngestFundingPayments(ctx context.Context, batch FundingIngestBatch) ([]FundingPaymentFact, SyncWatermark, error) {
	if j == nil || j.store == nil {
		return nil, SyncWatermark{}, fmt.Errorf("ledger journal store is required")
	}
	if err := validateFundingIngestBatch(batch); err != nil {
		return nil, SyncWatermark{}, err
	}
	return j.store.ingestFundingPayments(ctx, batch)
}

func validateFundingIngestBatch(batch FundingIngestBatch) error {
	if err := validateScope(batch.Scope); err != nil {
		return err
	}
	if !sameExactScope(batch.Scope, batch.Watermark.Scope) {
		return ErrScopeMismatch
	}
	if !validStatus(batch.Watermark.Status) {
		return ErrCompletenessViolation
	}
	switch batch.Watermark.Status {
	case Complete:
		if len(batch.Payments) == 0 {
			return ErrCompletenessViolation
		}
	case CompleteEmpty:
		if len(batch.Payments) != 0 {
			return ErrCompletenessViolation
		}
	}
	if batch.Watermark.Verified && batch.Watermark.Status != Complete && batch.Watermark.Status != CompleteEmpty {
		return ErrCompletenessViolation
	}
	watermarkID := stableID("watermark", batch.Watermark.Scope, batch.Watermark.SourceRef, batch.Watermark.Revision+"|"+batch.Watermark.Cursor)
	for _, payment := range batch.Payments {
		if !sameExactScope(batch.Scope, payment.Scope) {
			return ErrScopeMismatch
		}
		if payment.SyncWatermarkID != "" && payment.SyncWatermarkID != watermarkID {
			return ErrScopeMismatch
		}
		if strings.TrimSpace(payment.Payment.Asset) == "" {
			return ErrCompletenessViolation
		}
	}
	return nil
}

func (j *Journal) AcquireLineage(ctx context.Context, fact LineageFact) (LineageFact, error) {
	if j == nil || j.store == nil {
		return LineageFact{}, fmt.Errorf("ledger journal store is required")
	}
	if err := validateLineageScope(fact.Scope); err != nil {
		return LineageFact{}, err
	}
	fact.Active = true
	fact.FactID = stableID("lineage", identityScope(fact.Scope), fact.LineageID+"|"+fact.Provenance.SourceRef, fact.Provenance.SourceRevision)
	hash := payloadHash(fact)
	stored, existed, err := j.store.acquireLineage(ctx, fact, hash)
	if err != nil {
		return LineageFact{}, err
	}
	if existed && payloadHash(stored) != hash {
		return LineageFact{}, ErrFactConflict
	}
	return stored, nil
}

func (j *Journal) AppendLineageRevision(ctx context.Context, fact LineageRevisionFact) (LineageRevisionFact, error) {
	return j.appendLineageRevision(ctx, fact, false)
}

func (j *Journal) appendLineageRevision(ctx context.Context, fact LineageRevisionFact, allowSupersession bool) (LineageRevisionFact, error) {
	if j == nil || j.store == nil {
		return LineageRevisionFact{}, fmt.Errorf("ledger journal store is required")
	}
	if fact.SupersedesFactID != "" && !allowSupersession {
		return LineageRevisionFact{}, ErrFactConflict
	}
	if err := validateLineageScope(fact.Scope); err != nil {
		return LineageRevisionFact{}, err
	}
	lineage, _, ok, err := j.store.getLineage(ctx, fact.LineageFactID)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if !ok {
		return LineageRevisionFact{}, ErrFactNotFound
	}
	if !sameLineageScope(lineage.Scope, fact.Scope) {
		return LineageRevisionFact{}, ErrScopeMismatch
	}
	fact.FactID = stableID("lineage_revision", identityScope(fact.Scope), fact.LineageFactID+"|"+fact.Revision+"|"+fact.Provenance.SourceRef, fact.Provenance.SourceRevision)
	hash := payloadHash(fact)
	stored, existed, err := j.store.appendLineageRevision(ctx, fact, hash)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if existed && payloadHash(stored) != hash {
		return LineageRevisionFact{}, ErrFactConflict
	}
	return stored, nil
}

func (j *Journal) AppendSupersedingLineageRevision(ctx context.Context, supersedesFactID string, fact LineageRevisionFact) (LineageRevisionFact, error) {
	target, _, ok, err := j.store.getLineageRevision(ctx, supersedesFactID)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if !ok {
		return LineageRevisionFact{}, ErrFactNotFound
	}
	if target.LineageFactID != fact.LineageFactID || !sameLineageScope(target.Scope, fact.Scope) {
		return LineageRevisionFact{}, ErrScopeMismatch
	}
	if compareRevision(fact.Provenance.SourceRevision, target.Provenance.SourceRevision) <= 0 {
		return LineageRevisionFact{}, ErrRevisionOrder
	}
	fact.SupersedesFactID = supersedesFactID
	return j.appendLineageRevision(ctx, fact, true)
}

func (j *Journal) ReleaseLineageWithRevision(ctx context.Context, factID string, revision LineageRevisionFact, closedAt time.Time) (LineageRevisionFact, error) {
	if j == nil || j.store == nil {
		return LineageRevisionFact{}, fmt.Errorf("ledger journal store is required")
	}
	if closedAt.IsZero() {
		return LineageRevisionFact{}, ErrCompletenessViolation
	}
	revision.LineageFactID = factID
	if strings.TrimSpace(revision.Status) == "" {
		revision.Status = "released"
	}
	if err := validateLineageScope(revision.Scope); err != nil {
		return LineageRevisionFact{}, err
	}
	lineage, _, ok, err := j.store.getLineage(ctx, factID)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if !ok {
		return LineageRevisionFact{}, ErrFactNotFound
	}
	if !sameLineageScope(lineage.Scope, revision.Scope) {
		return LineageRevisionFact{}, ErrScopeMismatch
	}
	revision.FactID = stableID("lineage_revision", identityScope(revision.Scope), revision.LineageFactID+"|"+revision.Revision+"|"+revision.Provenance.SourceRef, revision.Provenance.SourceRevision)
	hash := payloadHash(revision)
	return j.store.releaseLineageWithRevision(ctx, factID, revision, hash, closedAt.UTC())
}

func (j *Journal) Replay(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	entries, err := j.store.replay(ctx, scope)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, k int) bool {
		left := entries[i]
		right := entries[k]
		if c := compareRevision(left.Revision, right.Revision); c != 0 {
			return c < 0
		}
		if left.Marker != right.Marker {
			return left.Marker < right.Marker
		}
		return left.SequenceID < right.SequenceID
	})
	for i := range entries {
		switch entries[i].Kind {
		case "funding":
			entries[i].OrderKey = "fact:" + entries[i].Revision
		case "fill":
			entries[i].OrderKey = "fill:" + entries[i].Revision
		case "position":
			entries[i].OrderKey = "position:" + entries[i].Revision
		case "lineage":
			entries[i].OrderKey = "lineage:" + entries[i].Revision
		case "lineage_revision":
			entries[i].OrderKey = "lineage_revision:" + entries[i].Revision
		case "watermark":
			entries[i].OrderKey = "watermark:" + entries[i].Revision + ":" + entries[i].Marker
		}
	}
	return entries, nil
}

func (j *Journal) PortfolioScopes(ctx context.Context, principalID, mode, reportingAsset string) ([]Scope, error) {
	if j == nil || j.store == nil {
		return nil, fmt.Errorf("ledger journal store is required")
	}
	if strings.TrimSpace(principalID) == "" || (mode != "test" && mode != "live") || strings.TrimSpace(reportingAsset) == "" {
		return nil, ErrScopeMismatch
	}
	return j.store.portfolioScopes(ctx, principalID, mode, reportingAsset)
}

func stableID(kind string, scope Scope, sourceRef, revision string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		kind,
		scope.PrincipalID,
		scope.Mode,
		scope.AccountID,
		scope.InstrumentID,
		scope.WindowStart.UTC().Format(time.RFC3339Nano),
		scope.WindowEnd.UTC().Format(time.RFC3339Nano),
		scope.PaginationTraversal,
		sourceRef,
		revision,
	}, "\x00")))
	return kind + "_" + hex.EncodeToString(sum[:16])
}

func payloadHash(value any) string {
	body, _ := json.Marshal(canonicalPayload(value))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func canonicalPayload(value any) any {
	switch v := value.(type) {
	case FundingPaymentFact:
		return map[string]any{
			"supersedesFactId": v.SupersedesFactID,
			"syncWatermarkId":  v.SyncWatermarkID,
			"paymentId":        v.PaymentID,
			"positionId":       v.PositionID,
			"venue":            v.Venue,
			"venueEnvironment": v.VenueEnvironment,
			"product":          v.Product,
			"formulaVersion":   v.FormulaVersion,
			"scope":            canonicalScope(v.Scope),
			"provenance":       canonicalProvenance(v.Provenance),
			"payment":          canonicalMoney(v.Payment),
		}
	case FillCashflowFact:
		return map[string]any{
			"supersedesFactId": v.SupersedesFactID,
			"syncWatermarkId":  v.SyncWatermarkID,
			"scope":            canonicalScope(v.Scope),
			"positionId":       v.PositionID,
			"lineageId":        v.LineageID,
			"fillId":           v.FillID,
			"provenance":       canonicalProvenance(v.Provenance),
			"fee":              canonicalMoney(v.Fee),
			"rebate":           canonicalMoney(v.Rebate),
			"sourceAsset":      v.SourceAsset,
			"reportingAsset":   v.ReportingAsset,
			"conversion":       canonicalConversion(v.Conversion),
			"alreadySigned":    v.AlreadySigned,
		}
	case PositionSnapshotFact:
		return map[string]any{
			"supersedesFactId":      v.SupersedesFactID,
			"syncWatermarkId":       v.SyncWatermarkID,
			"scope":                 canonicalScope(v.Scope),
			"positionId":            v.PositionID,
			"provenance":            canonicalProvenance(v.Provenance),
			"closed":                v.Closed,
			"asOf":                  canonicalTime(v.AsOf),
			"reportingAsset":        v.ReportingAsset,
			"realizedTradingPnl":    v.RealizedTradingPnl.String(),
			"unrealizedTradingPnl":  v.UnrealizedTradingPnl.String(),
			"actualFundingCashFlow": v.ActualFundingCashFlow.String(),
			"fees":                  v.Fees.String(),
			"verified":              v.Verified,
			"residual":              canonicalResidual(v.Residual),
			"conversion":            canonicalConversion(v.Conversion),
		}
	case SyncWatermark:
		return map[string]any{
			"scope":            canonicalScope(v.Scope),
			"sourceRef":        v.SourceRef,
			"cursor":           v.Cursor,
			"revision":         v.Revision,
			"observedAt":       canonicalTime(v.ObservedAt),
			"effectiveThrough": canonicalTime(v.EffectiveThrough),
			"completeThrough":  canonicalTime(v.CompleteThrough),
			"status":           v.Status,
			"verified":         v.Verified,
		}
	case LineageFact:
		return map[string]any{
			"scope":            canonicalScope(identityScope(v.Scope)),
			"lineageId":        v.LineageID,
			"positionId":       v.PositionID,
			"venueEnvironment": v.VenueEnvironment,
			"product":          v.Product,
			"provenance":       canonicalProvenance(v.Provenance),
		}
	case LineageRevisionFact:
		return map[string]any{
			"supersedesFactId": v.SupersedesFactID,
			"syncWatermarkId":  v.SyncWatermarkID,
			"lineageFactId":    v.LineageFactID,
			"scope":            canonicalScope(identityScope(v.Scope)),
			"revision":         v.Revision,
			"status":           v.Status,
			"reason":           v.Reason,
			"provenance":       canonicalProvenance(v.Provenance),
		}
	default:
		return value
	}
}

func canonicalConversion(conversion ConversionMetadata) map[string]any {
	return map[string]any{
		"source":         conversion.Source,
		"asOf":           canonicalTime(conversion.AsOf),
		"roundingMode":   conversion.RoundingMode,
		"scale":          conversion.Scale,
		"formulaVersion": conversion.FormulaVersion,
	}
}

func canonicalResidual(residual Residual) map[string]any {
	return map[string]any{
		"legQuantity":            residual.LegQuantity.String(),
		"reportingAssetNotional": residual.ReportingAssetNotional.String(),
		"openOrderCount":         residual.OpenOrderCount,
		"pendingCompensation":    residual.PendingCompensation,
		"blockerClass":           residual.BlockerClass,
	}
}

func canonicalScope(scope Scope) map[string]any {
	return map[string]any{
		"principalId":         scope.PrincipalID,
		"mode":                scope.Mode,
		"accountId":           scope.AccountID,
		"instrumentId":        scope.InstrumentID,
		"windowStart":         canonicalTime(scope.WindowStart),
		"windowEnd":           canonicalTime(scope.WindowEnd),
		"paginationTraversal": scope.PaginationTraversal,
	}
}

func canonicalProvenance(provenance Provenance) map[string]any {
	return map[string]any{
		"sourceRef":      provenance.SourceRef,
		"sourceRevision": provenance.SourceRevision,
		"sourceVersion":  provenance.SourceVersion,
		"observedAt":     canonicalTime(provenance.ObservedAt),
		"effectiveAt":    canonicalTime(provenance.EffectiveAt),
	}
}

func canonicalMoney(money Money) map[string]any {
	return map[string]any{
		"asset":  money.Asset,
		"amount": money.Amount.String(),
	}
}

func canonicalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func compareRevision(left, right string) int {
	li, lerr := strconv.ParseInt(strings.TrimSpace(left), 10, 64)
	ri, rerr := strconv.ParseInt(strings.TrimSpace(right), 10, 64)
	if lerr == nil && rerr == nil {
		switch {
		case li < ri:
			return -1
		case li > ri:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(left, right)
}

func validStatus(status CompletenessStatus) bool {
	switch status {
	case Unsupported, TemporarilyUnavailable, Incomplete, CompleteEmpty, Complete:
		return true
	default:
		return false
	}
}

func sameExactScope(left, right Scope) bool {
	return left.PrincipalID == right.PrincipalID &&
		left.Mode == right.Mode &&
		left.AccountID == right.AccountID &&
		left.InstrumentID == right.InstrumentID &&
		left.WindowStart.Equal(right.WindowStart) &&
		left.WindowEnd.Equal(right.WindowEnd) &&
		left.PaginationTraversal == right.PaginationTraversal
}

func sameSyncWatermarkPayload(left, right SyncWatermark) bool {
	// ObservedAt is the Gateway read time and can advance without a source
	// revision change. The first persisted observation remains immutable.
	return sameExactScope(left.Scope, right.Scope) &&
		left.SourceRef == right.SourceRef &&
		left.Cursor == right.Cursor &&
		left.Revision == right.Revision &&
		left.EffectiveThrough.Equal(right.EffectiveThrough) &&
		left.CompleteThrough.Equal(right.CompleteThrough) &&
		left.Status == right.Status &&
		left.Verified == right.Verified
}

func sameFundingPaymentPayload(left, right FundingPaymentFact) bool {
	// ObservedAt is the Gateway read time and can advance when the same source
	// fact is replayed. The first persisted observation remains immutable.
	left.Provenance.ObservedAt = time.Time{}
	right.Provenance.ObservedAt = time.Time{}
	return payloadHash(left) == payloadHash(right)
}

func sameLineageScope(left, right Scope) bool {
	return left.PrincipalID == right.PrincipalID &&
		left.Mode == right.Mode &&
		left.AccountID == right.AccountID &&
		left.InstrumentID == right.InstrumentID
}

func identityScope(scope Scope) Scope {
	return Scope{
		PrincipalID:  scope.PrincipalID,
		Mode:         scope.Mode,
		AccountID:    scope.AccountID,
		InstrumentID: scope.InstrumentID,
	}
}

func scopeKey(scope Scope) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s", scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID)
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.PrincipalID) == "" ||
		(scope.Mode != "test" && scope.Mode != "live") ||
		strings.TrimSpace(scope.AccountID) == "" ||
		strings.TrimSpace(scope.InstrumentID) == "" ||
		scope.WindowStart.IsZero() ||
		scope.WindowEnd.IsZero() ||
		!scope.WindowStart.Before(scope.WindowEnd) ||
		strings.TrimSpace(scope.PaginationTraversal) == "" {
		return ErrScopeMismatch
	}
	return nil
}

func validateLineageScope(scope Scope) error {
	if strings.TrimSpace(scope.PrincipalID) == "" ||
		(scope.Mode != "test" && scope.Mode != "live") ||
		strings.TrimSpace(scope.AccountID) == "" ||
		strings.TrimSpace(scope.InstrumentID) == "" {
		return ErrScopeMismatch
	}
	return nil
}

type MemoryStore struct {
	mu         sync.Mutex
	sequence   int64
	funding    map[string]storedFunding
	fills      map[string]storedFill
	positions  map[string]storedPosition
	watermarks map[string]storedWatermark
	lineages   map[string]storedLineage
	revisions  map[string]storedLineageRevision
	active     map[string]string
	successors map[string]string
}

type storedFunding struct {
	fact FundingPaymentFact
	hash string
}

type storedFill struct {
	fill FillCashflowFact
	hash string
}

type storedPosition struct {
	position PositionSnapshotFact
	hash     string
}

type storedWatermark struct {
	watermark SyncWatermark
	hash      string
}

type storedLineage struct {
	lineage LineageFact
	hash    string
}

type storedLineageRevision struct {
	revision LineageRevisionFact
	hash     string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		funding:    map[string]storedFunding{},
		fills:      map[string]storedFill{},
		positions:  map[string]storedPosition{},
		watermarks: map[string]storedWatermark{},
		lineages:   map[string]storedLineage{},
		revisions:  map[string]storedLineageRevision{},
		active:     map[string]string{},
		successors: map[string]string{},
	}
}

func (s *MemoryStore) ingestFundingPayments(_ context.Context, batch FundingIngestBatch) ([]FundingPaymentFact, SyncWatermark, error) {
	if err := validateFundingIngestBatch(batch); err != nil {
		return nil, SyncWatermark{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	funding := make(map[string]storedFunding, len(s.funding)+len(batch.Payments))
	for id, stored := range s.funding {
		funding[id] = stored
	}
	watermarks := make(map[string]storedWatermark, len(s.watermarks)+1)
	for id, stored := range s.watermarks {
		watermarks[id] = stored
	}
	successors := make(map[string]string, len(s.successors)+len(batch.Payments))
	for id, successor := range s.successors {
		successors[id] = successor
	}
	sequence := s.sequence

	watermark := batch.Watermark
	watermark.FactID = stableID("watermark", watermark.Scope, watermark.SourceRef, watermark.Revision+"|"+watermark.Cursor)
	watermarkHash := payloadHash(watermark)
	if stored, ok := watermarks[watermark.FactID]; ok {
		if !sameSyncWatermarkPayload(stored.watermark, watermark) {
			return nil, SyncWatermark{}, ErrFactConflict
		}
		watermark = stored.watermark
	} else {
		sequence++
		watermark.SequenceID = sequence
		watermarks[watermark.FactID] = storedWatermark{watermark: watermark, hash: watermarkHash}
	}

	items := make([]FundingPaymentFact, 0, len(batch.Payments))
	for _, input := range batch.Payments {
		fact := input
		fact.SyncWatermarkID = watermark.FactID
		if fact.SupersedesFactID != "" {
			target, ok := funding[fact.SupersedesFactID]
			if !ok {
				return nil, SyncWatermark{}, ErrFactNotFound
			}
			if !sameExactScope(target.fact.Scope, fact.Scope) {
				return nil, SyncWatermark{}, ErrScopeMismatch
			}
			if compareRevision(fact.Provenance.SourceRevision, target.fact.Provenance.SourceRevision) <= 0 {
				return nil, SyncWatermark{}, ErrRevisionOrder
			}
		}
		fact.FactID = stableID("funding", fact.Scope, fact.Provenance.SourceRef, fact.Provenance.SourceRevision)
		hash := payloadHash(fact)
		if stored, ok := funding[fact.FactID]; ok {
			if !sameFundingPaymentPayload(stored.fact, fact) {
				return nil, SyncWatermark{}, ErrFactConflict
			}
			items = append(items, stored.fact)
			continue
		}
		if fact.SupersedesFactID != "" && successors["funding:"+fact.SupersedesFactID] != "" {
			return nil, SyncWatermark{}, ErrFactConflict
		}
		sequence++
		fact.SequenceID = sequence
		funding[fact.FactID] = storedFunding{fact: fact, hash: hash}
		if fact.SupersedesFactID != "" {
			successors["funding:"+fact.SupersedesFactID] = fact.FactID
		}
		items = append(items, fact)
	}
	actual := 0
	for _, stored := range funding {
		if stored.fact.SyncWatermarkID == watermark.FactID {
			actual++
		}
	}
	if actual != deduplicatedFundingFactCount(batch.Payments) {
		return nil, SyncWatermark{}, ErrCompletenessViolation
	}

	s.funding = funding
	s.watermarks = watermarks
	s.successors = successors
	s.sequence = sequence
	return items, watermark, nil
}

func (s *MemoryStore) Exec(_ context.Context, sql string, _ ...any) (int64, error) {
	lower := strings.ToLower(strings.TrimSpace(sql))
	if strings.HasPrefix(lower, "update ledger_funding_payment_facts") ||
		strings.HasPrefix(lower, "delete from ledger_funding_payment_facts") ||
		strings.HasPrefix(lower, "update ledger_fill_cashflow_facts") ||
		strings.HasPrefix(lower, "delete from ledger_fill_cashflow_facts") ||
		strings.HasPrefix(lower, "update ledger_position_snapshot_facts") ||
		strings.HasPrefix(lower, "delete from ledger_position_snapshot_facts") ||
		strings.HasPrefix(lower, "update ledger_lineage_facts set position_id") ||
		strings.HasPrefix(lower, "delete from ledger_lineage_facts") ||
		strings.HasPrefix(lower, "update ledger_lineage_revision_facts") ||
		strings.HasPrefix(lower, "delete from ledger_lineage_revision_facts") ||
		strings.HasPrefix(lower, "update ledger_sync_watermarks") ||
		strings.HasPrefix(lower, "delete from ledger_sync_watermarks") {
		return 0, ErrAppendOnlyViolation
	}
	return 0, nil
}

func (s *MemoryStore) appendFunding(_ context.Context, fact FundingPaymentFact, hash string) (FundingPaymentFact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.funding[fact.FactID]; ok {
		return stored.fact, true, nil
	}
	if fact.SupersedesFactID != "" && s.successors["funding:"+fact.SupersedesFactID] != "" {
		return FundingPaymentFact{}, false, ErrFactConflict
	}
	s.sequence++
	fact.SequenceID = s.sequence
	s.funding[fact.FactID] = storedFunding{fact: fact, hash: hash}
	if fact.SupersedesFactID != "" {
		s.successors["funding:"+fact.SupersedesFactID] = fact.FactID
	}
	return fact, false, nil
}

func (s *MemoryStore) getFunding(_ context.Context, factID string) (FundingPaymentFact, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.funding[factID]
	return stored.fact, stored.hash, ok, nil
}

func (s *MemoryStore) appendFill(_ context.Context, fact FillCashflowFact, hash string) (FillCashflowFact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.fills[fact.FactID]; ok {
		return stored.fill, true, nil
	}
	if fact.SupersedesFactID != "" && s.successors["fill:"+fact.SupersedesFactID] != "" {
		return FillCashflowFact{}, false, ErrFactConflict
	}
	s.sequence++
	fact.SequenceID = s.sequence
	s.fills[fact.FactID] = storedFill{fill: fact, hash: hash}
	if fact.SupersedesFactID != "" {
		s.successors["fill:"+fact.SupersedesFactID] = fact.FactID
	}
	return fact, false, nil
}

func (s *MemoryStore) getFill(_ context.Context, factID string) (FillCashflowFact, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.fills[factID]
	return stored.fill, stored.hash, ok, nil
}

func (s *MemoryStore) appendPosition(_ context.Context, fact PositionSnapshotFact, hash string) (PositionSnapshotFact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.positions[fact.FactID]; ok {
		return stored.position, true, nil
	}
	if fact.SupersedesFactID != "" && s.successors["position:"+fact.SupersedesFactID] != "" {
		return PositionSnapshotFact{}, false, ErrFactConflict
	}
	s.sequence++
	fact.SequenceID = s.sequence
	s.positions[fact.FactID] = storedPosition{position: fact, hash: hash}
	if fact.SupersedesFactID != "" {
		s.successors["position:"+fact.SupersedesFactID] = fact.FactID
	}
	return fact, false, nil
}

func (s *MemoryStore) getPosition(_ context.Context, factID string) (PositionSnapshotFact, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.positions[factID]
	return stored.position, stored.hash, ok, nil
}

func (s *MemoryStore) appendWatermark(_ context.Context, watermark SyncWatermark, hash string) (SyncWatermark, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.watermarks[watermark.FactID]; ok {
		return stored.watermark, true, nil
	}
	s.sequence++
	watermark.SequenceID = s.sequence
	s.watermarks[watermark.FactID] = storedWatermark{watermark: watermark, hash: hash}
	return watermark, false, nil
}

func (s *MemoryStore) acquireLineage(_ context.Context, lineage LineageFact, hash string) (LineageFact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.lineages[lineage.FactID]; ok {
		return stored.lineage, true, nil
	}
	key := scopeKey(lineage.Scope)
	if s.active[key] != "" {
		return LineageFact{}, false, ErrActiveLineageExists
	}
	s.sequence++
	lineage.SequenceID = s.sequence
	lineage.Active = true
	s.lineages[lineage.FactID] = storedLineage{lineage: lineage, hash: hash}
	s.active[key] = lineage.FactID
	return lineage, false, nil
}

func (s *MemoryStore) getLineage(_ context.Context, factID string) (LineageFact, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.lineages[factID]
	return stored.lineage, stored.hash, ok, nil
}

func (s *MemoryStore) appendLineageRevision(_ context.Context, fact LineageRevisionFact, hash string) (LineageRevisionFact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.revisions[fact.FactID]; ok {
		return stored.revision, true, nil
	}
	if fact.SupersedesFactID != "" && s.successors["lineage_revision:"+fact.SupersedesFactID] != "" {
		return LineageRevisionFact{}, false, ErrFactConflict
	}
	s.sequence++
	fact.SequenceID = s.sequence
	s.revisions[fact.FactID] = storedLineageRevision{revision: fact, hash: hash}
	if fact.SupersedesFactID != "" {
		s.successors["lineage_revision:"+fact.SupersedesFactID] = fact.FactID
	}
	return fact, false, nil
}

func (s *MemoryStore) getLineageRevision(_ context.Context, factID string) (LineageRevisionFact, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.revisions[factID]
	return stored.revision, stored.hash, ok, nil
}

func (s *MemoryStore) releaseLineageWithRevision(_ context.Context, factID string, revision LineageRevisionFact, hash string, closedAt time.Time) (LineageRevisionFact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lineage, ok := s.lineages[factID]
	if !ok {
		return LineageRevisionFact{}, ErrFactNotFound
	}
	if stored, exists := s.revisions[revision.FactID]; exists {
		if stored.hash != hash || lineage.lineage.Active || !lineage.lineage.ClosedAt.Equal(closedAt) {
			return LineageRevisionFact{}, ErrFactConflict
		}
		return stored.revision, nil
	}
	if !lineage.lineage.Active {
		return LineageRevisionFact{}, ErrFactConflict
	}
	if revision.SupersedesFactID != "" && s.successors["lineage_revision:"+revision.SupersedesFactID] != "" {
		return LineageRevisionFact{}, ErrFactConflict
	}
	s.sequence++
	revision.SequenceID = s.sequence
	s.revisions[revision.FactID] = storedLineageRevision{revision: revision, hash: hash}
	if revision.SupersedesFactID != "" {
		s.successors["lineage_revision:"+revision.SupersedesFactID] = revision.FactID
	}
	lineage.lineage.Active = false
	lineage.lineage.ClosedAt = closedAt
	s.lineages[factID] = lineage
	if s.active[scopeKey(lineage.lineage.Scope)] == factID {
		delete(s.active, scopeKey(lineage.lineage.Scope))
	}
	return revision, nil
}

func (s *MemoryStore) replay(_ context.Context, scope Scope) ([]ReplayEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ReplayEntry
	for _, stored := range s.funding {
		fact := stored.fact
		if sameExactScope(scope, fact.Scope) {
			copy := fact
			entries = append(entries, ReplayEntry{Kind: "funding", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Funding: &copy})
		}
	}
	for _, stored := range s.fills {
		fact := stored.fill
		if sameExactScope(scope, fact.Scope) {
			copy := fact
			entries = append(entries, ReplayEntry{Kind: "fill", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Fill: &copy})
		}
	}
	for _, stored := range s.positions {
		fact := stored.position
		if sameExactScope(scope, fact.Scope) {
			copy := fact
			entries = append(entries, ReplayEntry{Kind: "position", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Position: &copy})
		}
	}
	for _, stored := range s.lineages {
		fact := stored.lineage
		if scopeKey(scope) == scopeKey(fact.Scope) {
			copy := fact
			entries = append(entries, ReplayEntry{Kind: "lineage", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Lineage: &copy})
		}
	}
	for _, stored := range s.revisions {
		fact := stored.revision
		if scopeKey(scope) == scopeKey(fact.Scope) {
			copy := fact
			entries = append(entries, ReplayEntry{Kind: "lineage_revision", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, RevisionFact: &copy})
		}
	}
	for _, stored := range s.watermarks {
		watermark := stored.watermark
		if sameExactScope(scope, watermark.Scope) {
			copy := watermark
			entries = append(entries, ReplayEntry{Kind: "watermark", Revision: watermark.Revision, Marker: watermark.Cursor, SequenceID: watermark.SequenceID, Watermark: &copy})
		}
	}
	return entries, nil
}

func (s *MemoryStore) portfolioScopes(_ context.Context, principalID, mode, reportingAsset string) ([]Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unique := map[string]Scope{}
	for _, stored := range s.positions {
		fact := stored.position
		if fact.Scope.PrincipalID != principalID || fact.Scope.Mode != mode || fact.ReportingAsset != reportingAsset {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
			fact.Scope.PrincipalID, fact.Scope.Mode, fact.Scope.AccountID, fact.Scope.InstrumentID,
			fact.Scope.WindowStart.UTC().Format(time.RFC3339Nano), fact.Scope.WindowEnd.UTC().Format(time.RFC3339Nano),
			fact.Scope.PaginationTraversal)
		unique[key] = fact.Scope
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	scopes := make([]Scope, 0, len(keys))
	for _, key := range keys {
		scopes = append(scopes, unique[key])
	}
	return scopes, nil
}
