package ledgerreconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersync"
)

var (
	ErrServiceDependency      = errors.New("ledger reconcile service dependency is missing")
	ErrFundingNotFinal        = errors.New("ledger reconcile funding sync is not final")
	ErrWatermarkScopeMismatch = errors.New("ledger reconcile watermark scope mismatch")
	ErrWatermarkBinding       = errors.New("ledger reconcile watermark binding is invalid")
)

type FundingSyncer interface {
	SyncTarget(context.Context, ledgersync.SyncTarget) (ledgersync.SyncResult, error)
}

type PortfolioProjector interface {
	RebuildPortfolioFromReplay(context.Context, ledgerprojection.AuthoritativePortfolioReplay) ([]ledgerprojection.ClosedPositionProjection, ledgerprojection.PortfolioProjection, error)
}

type Service struct {
	syncer    FundingSyncer
	journal   *ledgerfacts.Journal
	projector PortfolioProjector
}

type ReconcileResult struct {
	Bridge     Bridge
	Scopes     []ledgerfacts.Scope
	Watermarks []ledgerfacts.SyncWatermark
	Closed     []ledgerprojection.ClosedPositionProjection
	Portfolio  ledgerprojection.PortfolioProjection
}

func NewService(syncer FundingSyncer, journal *ledgerfacts.Journal, projector PortfolioProjector) *Service {
	return &Service{syncer: syncer, journal: journal, projector: projector}
}

func (s *Service) ReconcileClosedPosition(ctx context.Context, open, close execution.Execution, reportingAsset string) (ReconcileResult, error) {
	if s == nil || s.syncer == nil || s.journal == nil || s.projector == nil {
		return ReconcileResult{}, ErrServiceDependency
	}
	bridge, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: reportingAsset})
	if err != nil {
		return ReconcileResult{}, err
	}

	result := ReconcileResult{Bridge: bridge}
	var reconcileErr error
	for _, leg := range bridge.Legs {
		syncResult, err := s.syncer.SyncTarget(ctx, leg.SyncTarget)
		watermark := syncResult.Watermark
		if err != nil {
			if validateReturnedWatermark(leg.SyncTarget, watermark) != nil {
				return result, err
			}
			reconcileErr = errors.Join(reconcileErr, ErrFundingNotFinal, err)
		} else if err := validateReturnedWatermark(leg.SyncTarget, watermark); err != nil {
			return result, err
		}
		if !verifiedThrough(watermark) {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("%w: %s verified=%v", ErrFundingNotFinal, watermark.Status, watermark.Verified))
		}
		if err := s.validateFundingEvidence(ctx, watermark); err != nil {
			return result, err
		}
		if err := s.commitLegFacts(ctx, leg, watermark); err != nil {
			return result, err
		}
		result.Watermarks = append(result.Watermarks, watermark)
		result.Scopes = append(result.Scopes, watermark.Scope)
	}

	manifest, err := s.journal.PortfolioScopes(ctx, bridge.PrincipalID, bridge.Mode, bridge.ReportingAsset)
	if err != nil {
		return result, err
	}
	result.Scopes = manifest
	entries, err := s.replayManifest(ctx, result.Scopes)
	if err != nil {
		return result, err
	}
	closed, portfolio, err := s.projector.RebuildPortfolioFromReplay(ctx, ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    bridge.PrincipalID,
		Mode:           bridge.Mode,
		ReportingAsset: bridge.ReportingAsset,
		Scopes:         result.Scopes,
		Entries:        entries,
	})
	if err != nil {
		return result, err
	}
	result.Closed = closed
	result.Portfolio = portfolio
	return result, reconcileErr
}

func (s *Service) commitLegFacts(ctx context.Context, leg LegBridge, watermark ledgerfacts.SyncWatermark) error {
	lineage := leg.Lineage
	lineage.Scope = watermark.Scope
	revision := leg.LineageRevision
	revision.Scope = watermark.Scope
	revision.SyncWatermarkID = watermark.FactID

	existing, err := s.findHistoricalLineage(ctx, watermark.Scope, lineage.LineageID, lineage.PositionID)
	if err != nil {
		return err
	}
	var acquired ledgerfacts.LineageFact
	if existing != nil {
		acquired = *existing
	} else {
		acquired, err = s.journal.AcquireLineage(ctx, lineage)
		if err != nil {
			return err
		}
	}
	revision.LineageFactID = acquired.FactID

	for _, fill := range leg.FillCashflows {
		fill.Scope = watermark.Scope
		fill.SyncWatermarkID = watermark.FactID
		if _, err := s.journal.AppendFillCashflow(ctx, fill); err != nil {
			return err
		}
	}

	position := leg.PositionSnapshot
	position.Scope = watermark.Scope
	position.SyncWatermarkID = watermark.FactID
	position.Verified = watermark.Verified
	if _, err := s.journal.AppendPositionSnapshot(ctx, position); err != nil {
		return err
	}

	if acquired.Active {
		_, err = s.journal.ReleaseLineageWithRevision(ctx, acquired.FactID, revision, leg.Lineage.ClosedAt)
		return err
	}
	return s.appendSupersedingLineageRevision(ctx, acquired.FactID, revision)
}

func (s *Service) findHistoricalLineage(ctx context.Context, scope ledgerfacts.Scope, lineageID, positionID string) (*ledgerfacts.LineageFact, error) {
	entries, err := s.journal.Replay(ctx, scope)
	if err != nil {
		return nil, err
	}
	var selected *ledgerfacts.LineageFact
	for _, entry := range entries {
		if entry.Lineage == nil {
			continue
		}
		lineage := entry.Lineage
		if lineage.LineageID != lineageID {
			continue
		}
		if !sameServiceScope(scope, lineage.Scope) || lineage.PositionID != positionID {
			continue
		}
		item := *lineage
		if selected == nil {
			selected = &item
			continue
		}
		switch {
		case item.Active:
			selected = &item
		case !selected.Active && item.SequenceID > selected.SequenceID:
			selected = &item
		}
	}
	if selected == nil {
		return nil, nil
	}
	return selected, nil
}

func (s *Service) appendSupersedingLineageRevision(ctx context.Context, lineageFactID string, revision ledgerfacts.LineageRevisionFact) error {
	entries, err := s.journal.Replay(ctx, revision.Scope)
	if err != nil {
		return err
	}
	var latest *ledgerfacts.LineageRevisionFact
	for _, entry := range entries {
		if entry.RevisionFact == nil || entry.RevisionFact.LineageFactID != lineageFactID {
			continue
		}
		item := *entry.RevisionFact
		if latest == nil || item.SequenceID > latest.SequenceID {
			latest = &item
		}
	}
	if latest == nil {
		return ledgerfacts.ErrFactNotFound
	}
	revision.LineageFactID = lineageFactID
	if _, err := s.journal.AppendSupersedingLineageRevision(ctx, latest.FactID, revision); err == nil {
		return nil
	}
	_, err = s.journal.AppendLineageRevision(ctx, revision)
	return err
}

func (s *Service) replayManifest(ctx context.Context, scopes []ledgerfacts.Scope) ([]ledgerfacts.ReplayEntry, error) {
	var entries []ledgerfacts.ReplayEntry
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		replayed, err := s.journal.Replay(ctx, scope)
		if err != nil {
			return nil, err
		}
		for _, entry := range replayed {
			kind, factID := replayIdentity(entry)
			if kind == "" || factID == "" {
				return nil, ErrWatermarkBinding
			}
			key := kind + "\x00" + factID
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func replayIdentity(entry ledgerfacts.ReplayEntry) (string, string) {
	switch {
	case entry.Funding != nil:
		return "funding", entry.Funding.FactID
	case entry.Fill != nil:
		return "fill", entry.Fill.FactID
	case entry.Position != nil:
		return "position", entry.Position.FactID
	case entry.Lineage != nil:
		return "lineage", entry.Lineage.FactID
	case entry.RevisionFact != nil:
		return "lineage_revision", entry.RevisionFact.FactID
	case entry.Watermark != nil:
		return "watermark", entry.Watermark.FactID
	default:
		return "", ""
	}
}

func (s *Service) validateFundingEvidence(ctx context.Context, watermark ledgerfacts.SyncWatermark) error {
	entries, err := s.journal.Replay(ctx, watermark.Scope)
	if err != nil {
		return err
	}
	fundingSeen := false
	for _, entry := range entries {
		if entry.Funding == nil {
			continue
		}
		fundingSeen = true
		if entry.Funding.SyncWatermarkID != watermark.FactID || !sameServiceScope(entry.Funding.Scope, watermark.Scope) {
			return ErrWatermarkBinding
		}
	}
	switch watermark.Status {
	case ledgerfacts.Complete:
		if !fundingSeen {
			return ErrWatermarkBinding
		}
	case ledgerfacts.CompleteEmpty:
		if fundingSeen {
			return ErrWatermarkBinding
		}
	}
	return nil
}

func validateReturnedWatermark(target ledgersync.SyncTarget, watermark ledgerfacts.SyncWatermark) error {
	if watermark.FactID == "" || watermark.Scope.PaginationTraversal == "" {
		return ErrWatermarkBinding
	}
	if watermark.Scope.PrincipalID != target.PrincipalID ||
		watermark.Scope.Mode != target.Mode ||
		watermark.Scope.AccountID != target.AccountID ||
		watermark.Scope.InstrumentID != target.InstrumentID ||
		!watermark.Scope.WindowStart.Equal(target.WindowStart.UTC()) ||
		!watermark.Scope.WindowEnd.Equal(target.WindowEnd.UTC()) {
		return ErrWatermarkScopeMismatch
	}
	if !validFundingStatus(watermark.Status) {
		return ErrWatermarkBinding
	}
	return nil
}

func verifiedThrough(watermark ledgerfacts.SyncWatermark) bool {
	return finalFundingStatus(watermark.Status) && watermark.Verified
}

func finalFundingStatus(status ledgerfacts.CompletenessStatus) bool {
	return status == ledgerfacts.Complete || status == ledgerfacts.CompleteEmpty
}

func validFundingStatus(status ledgerfacts.CompletenessStatus) bool {
	switch status {
	case ledgerfacts.Unsupported, ledgerfacts.TemporarilyUnavailable, ledgerfacts.Incomplete, ledgerfacts.CompleteEmpty, ledgerfacts.Complete:
		return true
	default:
		return false
	}
}

func sameServiceScope(left, right ledgerfacts.Scope) bool {
	return left.PrincipalID == right.PrincipalID &&
		left.Mode == right.Mode &&
		left.AccountID == right.AccountID &&
		left.InstrumentID == right.InstrumentID &&
		left.WindowStart.Equal(right.WindowStart) &&
		left.WindowEnd.Equal(right.WindowEnd) &&
		left.PaginationTraversal == right.PaginationTraversal
}
