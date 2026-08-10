package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

var (
	ErrLedgerRecoveryDependency    = errors.New("ledger recovery dependency is missing")
	ErrLedgerRecoveryParentMissing = errors.New("ledger recovery close execution parent is missing")
	ErrLedgerRecoveryOpenMissing   = errors.New("ledger recovery open execution is missing")
)

type LedgerRecoveryResult struct {
	Scanned    int
	Eligible   int
	Reconciled int
	Incomplete int
	Failed     int
	Errors     []LedgerRecoveryItemError
}

type LedgerRecoveryItemError struct {
	ExecutionID       string
	ParentExecutionID string
	Err               error
}

func (err LedgerRecoveryItemError) Error() string {
	if strings.TrimSpace(err.ParentExecutionID) == "" {
		return fmt.Sprintf("ledger recovery failed for close execution %q", err.ExecutionID)
	}
	return fmt.Sprintf("ledger recovery failed for close execution %q parent %q", err.ExecutionID, err.ParentExecutionID)
}

func (err LedgerRecoveryItemError) Unwrap() error {
	return err.Err
}

type LedgerRecoveryError struct {
	Items []LedgerRecoveryItemError
}

func (err LedgerRecoveryError) Error() string {
	return fmt.Sprintf("ledger recovery failed for %d closed executions", len(err.Items))
}

func (err LedgerRecoveryError) Unwrap() []error {
	unwrapped := make([]error, 0, len(err.Items))
	for _, item := range err.Items {
		unwrapped = append(unwrapped, item)
	}
	return unwrapped
}

type LedgerRecoveryReconciler struct {
	Execution      executionmonitor.Reconciler
	Source         executionSnapshotSource
	Scope          readmodel.Scope
	Ledger         closedPositionLedgerReconciler
	ReportingAsset string
	OnLedgerError  func(error)
}

func (reconciler LedgerRecoveryReconciler) Recover(ctx context.Context) error {
	if reconciler.Execution == nil {
		return ErrLedgerRecoveryDependency
	}
	if err := reconciler.Execution.Recover(ctx); err != nil {
		return err
	}
	_, err := RecoverClosedPositionLedger(
		ctx,
		reconciler.Source,
		reconciler.Scope,
		reconciler.Ledger,
		reconciler.ReportingAsset,
	)
	if err != nil && reconciler.OnLedgerError != nil {
		reconciler.OnLedgerError(err)
	}
	return nil
}

func RecoverClosedPositionLedger(
	ctx context.Context,
	source executionSnapshotSource,
	scope readmodel.Scope,
	reconciler closedPositionLedgerReconciler,
	reportingAsset string,
) (LedgerRecoveryResult, error) {
	if source == nil || reconciler == nil {
		return LedgerRecoveryResult{}, ErrLedgerRecoveryDependency
	}
	reportingAsset = strings.TrimSpace(reportingAsset)
	if reportingAsset == "" {
		reportingAsset = "USDT"
	}
	items, err := source.ListExecutions(ctx, scope)
	if err != nil {
		return LedgerRecoveryResult{}, fmt.Errorf("ledger recovery list executions: %w", err)
	}
	items = append([]execution.Execution(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		return executionRecoveryLess(items[i], items[j])
	})

	result := LedgerRecoveryResult{Scanned: len(items)}
	opens := make(map[string]execution.Execution)
	for _, item := range items {
		if item.Operation == execution.OperationOpen {
			if strings.TrimSpace(item.ID) != "" {
				opens[item.ID] = item
			}
		}
	}
	for _, item := range items {
		if item.Operation != execution.OperationClose || item.State != execution.StateClosed {
			continue
		}
		result.Eligible++
		if strings.TrimSpace(item.ParentExecutionID) == "" {
			result.addError(item, ErrLedgerRecoveryParentMissing)
			continue
		}
		open, exists := opens[item.ParentExecutionID]
		if !exists {
			result.addError(item, ErrLedgerRecoveryOpenMissing)
			continue
		}
		reconcileResult, err := reconciler.ReconcileClosedPosition(ctx, open, item, reportingAsset)
		if isIncompleteLedgerRecovery(reconcileResult, err) {
			result.Incomplete++
			continue
		}
		if err != nil {
			result.addError(item, err)
			continue
		}
		result.Reconciled++
	}
	if len(result.Errors) > 0 {
		return result, LedgerRecoveryError{Items: append([]LedgerRecoveryItemError(nil), result.Errors...)}
	}
	return result, nil
}

func isIncompleteLedgerRecovery(result ledgerreconcile.ReconcileResult, err error) bool {
	return errors.Is(err, ledgerreconcile.ErrFundingNotFinal) &&
		hasLedgerProjectionResult(result) &&
		!errors.Is(err, ErrLedgerReconciliationScopeMismatch) &&
		!errors.Is(err, ErrLedgerReconciliationEventPublish)
}

func (result *LedgerRecoveryResult) addError(item execution.Execution, err error) {
	result.Failed++
	result.Errors = append(result.Errors, LedgerRecoveryItemError{
		ExecutionID:       item.ID,
		ParentExecutionID: item.ParentExecutionID,
		Err:               err,
	})
}

func executionRecoveryLess(left, right execution.Execution) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		if left.CreatedAt.IsZero() {
			return false
		}
		if right.CreatedAt.IsZero() {
			return true
		}
		return left.CreatedAt.Before(right.CreatedAt)
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		if left.UpdatedAt.IsZero() {
			return false
		}
		if right.UpdatedAt.IsZero() {
			return true
		}
		return left.UpdatedAt.Before(right.UpdatedAt)
	}
	return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
}
