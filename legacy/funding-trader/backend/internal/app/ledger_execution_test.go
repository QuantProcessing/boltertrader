package app

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
)

func TestLedgerExecutionAPIReconcilesClosedExecutionWithoutMaskingSuccessfulRiskReduction(t *testing.T) {
	close := execution.Execution{
		ID: "close-1", Operation: execution.OperationClose, State: execution.StateClosed,
		ParentExecutionID: "open-1", PositionID: "position-1",
	}
	inner := &ledgerExecutionFake{
		closeResult: executionservice.Result{Execution: close},
		getResult:   execution.Execution{ID: "open-1", Operation: execution.OperationOpen, State: execution.StateHedged},
	}
	reconciler := &ledgerReconcilerFake{err: ledgerreconcile.ErrFundingNotFinal}
	api := ledgerExecutionAPI{inner: inner, reconciler: reconciler, reportingAsset: "USDT"}

	result, err := api.Close(context.Background(), executionservice.Scope{PrincipalID: "principal-1", Mode: "test"}, executionservice.SubmitRequest{}, "idem", "request")
	if err != nil {
		t.Fatalf("successful close was masked by ledger state: %v", err)
	}
	if !result.Degraded || !result.Recoverable {
		t.Fatalf("pending ledger must be visible as recoverable degradation: %+v", result)
	}
	if inner.getID != "open-1" || reconciler.calls != 1 || reconciler.reportingAsset != "USDT" ||
		reconciler.open.ID != "open-1" || reconciler.close.ID != "close-1" {
		t.Fatalf("reconciliation inputs inner=%+v reconciler=%+v", inner, reconciler)
	}
}

func TestLedgerExecutionAPIDoesNotReconcileFailedOrNonClosedExecution(t *testing.T) {
	scope := executionservice.Scope{PrincipalID: "principal-1", Mode: "test"}
	closeErr := errors.New("close failed")
	inner := &ledgerExecutionFake{closeErr: closeErr}
	reconciler := &ledgerReconcilerFake{}
	api := ledgerExecutionAPI{inner: inner, reconciler: reconciler, reportingAsset: "USDT"}
	if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "idem", "request"); !errors.Is(err, closeErr) {
		t.Fatalf("close err=%v", err)
	}
	if reconciler.calls != 0 {
		t.Fatalf("failed close reconciled %d times", reconciler.calls)
	}

	inner.closeErr = nil
	inner.closeResult = executionservice.Result{Execution: execution.Execution{ID: "close-2", State: execution.StateUncertain}}
	if result, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "idem-2", "request-2"); err != nil || result.Degraded {
		t.Fatalf("non-closed result=%+v err=%v", result, err)
	}
	if reconciler.calls != 0 {
		t.Fatalf("uncertain close reconciled %d times", reconciler.calls)
	}
}

type ledgerExecutionFake struct {
	closeResult executionservice.Result
	closeErr    error
	getResult   execution.Execution
	getErr      error
	getID       string
}

func (*ledgerExecutionFake) Preview(context.Context, executionservice.Scope, executionservice.PreviewRequest) (execution.PreviewResult, error) {
	return execution.PreviewResult{}, nil
}

func (*ledgerExecutionFake) Submit(context.Context, executionservice.Scope, executionservice.SubmitRequest, string, string) (executionservice.Result, error) {
	return executionservice.Result{}, nil
}

func (fake *ledgerExecutionFake) Close(context.Context, executionservice.Scope, executionservice.SubmitRequest, string, string) (executionservice.Result, error) {
	return fake.closeResult, fake.closeErr
}

func (fake *ledgerExecutionFake) Get(_ context.Context, _ executionservice.Scope, id string) (execution.Execution, error) {
	fake.getID = id
	return fake.getResult, fake.getErr
}

type ledgerReconcilerFake struct {
	calls          int
	open           execution.Execution
	close          execution.Execution
	reportingAsset string
	err            error
}

func (fake *ledgerReconcilerFake) ReconcileClosedPosition(_ context.Context, open, close execution.Execution, reportingAsset string) (ledgerreconcile.ReconcileResult, error) {
	fake.calls++
	fake.open = open
	fake.close = close
	fake.reportingAsset = reportingAsset
	return ledgerreconcile.ReconcileResult{}, fake.err
}
