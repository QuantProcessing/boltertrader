package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestRecoverClosedPositionLedgerSortsAndReconcilesClosedCloseExecutions(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	openOne := ledgerRecoveryExecution("open-1", execution.OperationOpen, execution.StateHedged, "", now)
	openTwo := ledgerRecoveryExecution("open-2", execution.OperationOpen, execution.StateHedged, "", now.Add(time.Minute))
	closeLate := ledgerRecoveryExecution("close-late", execution.OperationClose, execution.StateClosed, "open-2", now.Add(4*time.Minute))
	closeEarly := ledgerRecoveryExecution("close-early", execution.OperationClose, execution.StateClosed, "open-1", now.Add(2*time.Minute))
	source := ledgerRecoverySource{items: []execution.Execution{
		closeLate,
		ledgerRecoveryExecution("close-open-state", execution.OperationClose, execution.StateHedged, "open-1", now.Add(3*time.Minute)),
		openTwo,
		ledgerRecoveryExecution("open-closed-state", execution.OperationOpen, execution.StateClosed, "", now.Add(time.Minute)),
		closeEarly,
		openOne,
	}}
	reconciler := &recordingLedgerHistoryReconciler{}

	result, err := RecoverClosedPositionLedger(context.Background(), source, readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, reconciler, "USD")
	if err != nil {
		t.Fatalf("RecoverClosedPositionLedger err = %v", err)
	}
	if result.Scanned != 6 || result.Eligible != 2 || result.Reconciled != 2 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := reconciler.callSummary(); got != "open-1/close-early/USD,open-2/close-late/USD" {
		t.Fatalf("reconcile order = %s", got)
	}
}

func TestRecoverClosedPositionLedgerCollectsItemErrorsAndContinues(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	reconcileErr := errors.New("funding sync unavailable secret=raw-payload")
	source := ledgerRecoverySource{items: []execution.Execution{
		ledgerRecoveryExecution("open-ok", execution.OperationOpen, execution.StateHedged, "", now),
		ledgerRecoveryExecution("open-fail", execution.OperationOpen, execution.StateHedged, "", now),
		ledgerRecoveryExecution("close-missing-parent", execution.OperationClose, execution.StateClosed, "", now.Add(time.Minute)),
		ledgerRecoveryExecution("close-missing-open", execution.OperationClose, execution.StateClosed, "open-missing", now.Add(2*time.Minute)),
		ledgerRecoveryExecution("close-fail", execution.OperationClose, execution.StateClosed, "open-fail", now.Add(3*time.Minute)),
		ledgerRecoveryExecution("close-spaced-parent", execution.OperationClose, execution.StateClosed, " open-ok ", now.Add(4*time.Minute)),
		ledgerRecoveryExecution("close-ok", execution.OperationClose, execution.StateClosed, "open-ok", now.Add(5*time.Minute)),
	}}
	reconciler := &recordingLedgerHistoryReconciler{errs: map[string]error{"close-fail": reconcileErr}}

	result, err := RecoverClosedPositionLedger(context.Background(), source, readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}, reconciler, "")
	if err == nil {
		t.Fatal("RecoverClosedPositionLedger err = nil")
	}
	if result.Scanned != 7 || result.Eligible != 5 || result.Reconciled != 1 || result.Failed != 4 {
		t.Fatalf("result = %+v", result)
	}
	if !errors.Is(err, ErrLedgerRecoveryParentMissing) || !errors.Is(err, ErrLedgerRecoveryOpenMissing) || !errors.Is(err, reconcileErr) {
		t.Fatalf("err does not unwrap diagnostic causes: %v", err)
	}
	var recoveryErr LedgerRecoveryError
	if !errors.As(err, &recoveryErr) || len(recoveryErr.Items) != 4 {
		t.Fatalf("err type = %T items=%+v", err, recoveryErr.Items)
	}
	if strings.Contains(err.Error(), "secret=raw-payload") {
		t.Fatalf("aggregate error leaked raw reconcile detail: %q", err.Error())
	}
	if got := reconciler.callSummary(); got != "open-fail/close-fail/USDT,open-ok/close-ok/USDT" {
		t.Fatalf("reconciler calls = %s", got)
	}
}

func TestRecoverClosedPositionLedgerTreatsProjectedFundingNotFinalAsIncomplete(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	source := ledgerRecoverySource{items: []execution.Execution{
		ledgerRecoveryExecution("open-1", execution.OperationOpen, execution.StateHedged, "", now),
		ledgerRecoveryExecution("close-1", execution.OperationClose, execution.StateClosed, "open-1", now.Add(time.Minute)),
	}}
	reconciler := &recordingLedgerHistoryReconciler{
		errs: map[string]error{"close-1": ledgerreconcile.ErrFundingNotFinal},
		results: map[string]ledgerreconcile.ReconcileResult{
			"close-1": {Portfolio: ledgerprojection.PortfolioProjection{ReportingAsset: "USDT"}},
		},
	}

	result, err := RecoverClosedPositionLedger(
		context.Background(),
		source,
		readmodel.Scope{PrincipalID: "principal-1", Mode: "test"},
		reconciler,
		"USDT",
	)
	if err != nil {
		t.Fatalf("projected funding finality gap must remain incomplete, not fail recovery: %v", err)
	}
	if result.Scanned != 2 || result.Eligible != 1 || result.Reconciled != 0 || result.Incomplete != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRecoverClosedPositionLedgerDoesNotMaskFundingFinalityInfrastructureFailures(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	source := ledgerRecoverySource{items: []execution.Execution{
		ledgerRecoveryExecution("open-1", execution.OperationOpen, execution.StateHedged, "", now),
		ledgerRecoveryExecution("close-1", execution.OperationClose, execution.StateClosed, "open-1", now.Add(time.Minute)),
	}}

	for _, test := range []struct {
		name   string
		result ledgerreconcile.ReconcileResult
		err    error
	}{
		{name: "missing projection", err: ledgerreconcile.ErrFundingNotFinal},
		{
			name:   "projection publish failure",
			result: ledgerreconcile.ReconcileResult{Portfolio: ledgerprojection.PortfolioProjection{ReportingAsset: "USDT"}},
			err:    errors.Join(ledgerreconcile.ErrFundingNotFinal, ErrLedgerReconciliationEventPublish),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler := &recordingLedgerHistoryReconciler{
				errs:    map[string]error{"close-1": test.err},
				results: map[string]ledgerreconcile.ReconcileResult{"close-1": test.result},
			}
			result, err := RecoverClosedPositionLedger(
				context.Background(),
				source,
				readmodel.Scope{PrincipalID: "principal-1", Mode: "test"},
				reconciler,
				"USDT",
			)
			if err == nil || !errors.Is(err, test.err) {
				t.Fatalf("err = %v, want infrastructure failure %v", err, test.err)
			}
			if result.Failed != 1 || result.Incomplete != 0 {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestLedgerRecoveryReconcilerRunsExecutionRecoveryBeforeLedgerHistory(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	order := []string{}
	executionRecover := &recordingExecutionRecover{order: &order}
	ledgerRecover := &recordingLedgerHistoryReconciler{order: &order}
	reconciler := LedgerRecoveryReconciler{
		Execution: executionRecover,
		Source: ledgerRecoverySource{items: []execution.Execution{
			ledgerRecoveryExecution("open-1", execution.OperationOpen, execution.StateHedged, "", now),
			ledgerRecoveryExecution("close-1", execution.OperationClose, execution.StateClosed, "open-1", now.Add(time.Minute)),
		}},
		Scope:          readmodel.Scope{PrincipalID: "principal-1", Mode: "test"},
		Ledger:         ledgerRecover,
		ReportingAsset: "USDC",
	}

	if err := reconciler.Recover(context.Background()); err != nil {
		t.Fatalf("Recover err = %v", err)
	}
	if got := strings.Join(order, ","); got != "execution,ledger:close-1" {
		t.Fatalf("recovery order = %s", got)
	}
	if got := ledgerRecover.callSummary(); got != "open-1/close-1/USDC" {
		t.Fatalf("ledger calls = %s", got)
	}
}

func TestLedgerRecoveryReconcilerSkipsLedgerWhenExecutionRecoveryFails(t *testing.T) {
	recoverErr := errors.New("execution recover failed")
	ledgerRecover := &recordingLedgerHistoryReconciler{}
	reconciler := LedgerRecoveryReconciler{
		Execution: &recordingExecutionRecover{err: recoverErr},
		Source:    ledgerRecoverySource{},
		Scope:     readmodel.Scope{PrincipalID: "principal-1", Mode: "test"},
		Ledger:    ledgerRecover,
	}

	if err := reconciler.Recover(context.Background()); !errors.Is(err, recoverErr) {
		t.Fatalf("Recover err = %v", err)
	}
	if ledgerRecover.callSummary() != "" {
		t.Fatalf("ledger recovery ran after execution failure: %s", ledgerRecover.callSummary())
	}
}

func TestLedgerRecoveryReconcilerReportsLedgerOnlyFailureWithoutFailingRecover(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	reconcileErr := errors.New("funding provisional")
	var reported error
	reconciler := LedgerRecoveryReconciler{
		Execution: &recordingExecutionRecover{},
		Source: ledgerRecoverySource{items: []execution.Execution{
			ledgerRecoveryExecution("open-1", execution.OperationOpen, execution.StateHedged, "", now),
			ledgerRecoveryExecution("close-1", execution.OperationClose, execution.StateClosed, "open-1", now.Add(time.Minute)),
		}},
		Scope:  readmodel.Scope{PrincipalID: "principal-1", Mode: "test"},
		Ledger: &recordingLedgerHistoryReconciler{errs: map[string]error{"close-1": reconcileErr}},
		OnLedgerError: func(err error) {
			reported = err
		},
	}

	if err := reconciler.Recover(context.Background()); err != nil {
		t.Fatalf("ledger-only failure must not fail monitor recovery: %v", err)
	}
	if !errors.Is(reported, reconcileErr) {
		t.Fatalf("reported ledger err = %v", reported)
	}
}

type ledgerRecoverySource struct {
	items []execution.Execution
	err   error
}

func (source ledgerRecoverySource) ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error) {
	return append([]execution.Execution(nil), source.items...), source.err
}

type recordingLedgerHistoryReconciler struct {
	calls   []ledgerRecoveryCall
	errs    map[string]error
	results map[string]ledgerreconcile.ReconcileResult
	order   *[]string
}

type ledgerRecoveryCall struct {
	openID         string
	closeID        string
	reportingAsset string
}

func (reconciler *recordingLedgerHistoryReconciler) ReconcileClosedPosition(_ context.Context, open, close execution.Execution, reportingAsset string) (ledgerreconcile.ReconcileResult, error) {
	reconciler.calls = append(reconciler.calls, ledgerRecoveryCall{
		openID: open.ID, closeID: close.ID, reportingAsset: reportingAsset,
	})
	if reconciler.order != nil {
		*reconciler.order = append(*reconciler.order, "ledger:"+close.ID)
	}
	result := ledgerreconcile.ReconcileResult{}
	if reconciler.results != nil {
		result = reconciler.results[close.ID]
	}
	if reconciler.errs != nil {
		return result, reconciler.errs[close.ID]
	}
	return result, nil
}

func (reconciler *recordingLedgerHistoryReconciler) callSummary() string {
	parts := make([]string, 0, len(reconciler.calls))
	for _, call := range reconciler.calls {
		parts = append(parts, call.openID+"/"+call.closeID+"/"+call.reportingAsset)
	}
	return strings.Join(parts, ",")
}

type recordingExecutionRecover struct {
	err   error
	order *[]string
}

func (reconciler *recordingExecutionRecover) Recover(context.Context) error {
	if reconciler.order != nil {
		*reconciler.order = append(*reconciler.order, "execution")
	}
	return reconciler.err
}

func ledgerRecoveryExecution(id string, operation execution.Operation, state execution.State, parentID string, createdAt time.Time) execution.Execution {
	return execution.Execution{
		ID: id, PrincipalID: "principal-1", Mode: "test",
		Operation: operation, State: state, ParentExecutionID: parentID,
		CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Second),
	}
}
