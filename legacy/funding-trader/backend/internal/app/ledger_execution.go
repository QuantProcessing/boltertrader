package app

import (
	"context"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
)

type closedPositionLedgerReconciler interface {
	ReconcileClosedPosition(context.Context, execution.Execution, execution.Execution, string) (ledgerreconcile.ReconcileResult, error)
}

type ledgerExecutionAPI struct {
	inner          executionAPI
	reconciler     closedPositionLedgerReconciler
	reportingAsset string
}

func (api ledgerExecutionAPI) Preview(ctx context.Context, scope executionservice.Scope, request executionservice.PreviewRequest) (execution.PreviewResult, error) {
	if api.inner == nil {
		return execution.PreviewResult{}, executionservice.ErrServiceUnavailable
	}
	return api.inner.Preview(ctx, scope, request)
}

func (api ledgerExecutionAPI) Submit(ctx context.Context, scope executionservice.Scope, request executionservice.SubmitRequest, idempotencyKey, requestID string) (executionservice.Result, error) {
	if api.inner == nil {
		return executionservice.Result{}, executionservice.ErrServiceUnavailable
	}
	return api.inner.Submit(ctx, scope, request, idempotencyKey, requestID)
}

func (api ledgerExecutionAPI) Close(ctx context.Context, scope executionservice.Scope, request executionservice.SubmitRequest, idempotencyKey, requestID string) (executionservice.Result, error) {
	if api.inner == nil {
		return executionservice.Result{}, executionservice.ErrServiceUnavailable
	}
	result, err := api.inner.Close(ctx, scope, request, idempotencyKey, requestID)
	if err != nil || result.Execution.State != execution.StateClosed {
		return result, err
	}
	parentID := strings.TrimSpace(result.Execution.ParentExecutionID)
	if parentID == "" || api.reconciler == nil {
		return ledgerPending(result), nil
	}
	open, getErr := api.inner.Get(ctx, scope, parentID)
	if getErr != nil {
		return ledgerPending(result), nil
	}
	reportingAsset := strings.TrimSpace(api.reportingAsset)
	if reportingAsset == "" {
		reportingAsset = "USDT"
	}
	if _, reconcileErr := api.reconciler.ReconcileClosedPosition(ctx, open, result.Execution, reportingAsset); reconcileErr != nil {
		return ledgerPending(result), nil
	}
	return result, nil
}

func (api ledgerExecutionAPI) Get(ctx context.Context, scope executionservice.Scope, id string) (execution.Execution, error) {
	if api.inner == nil {
		return execution.Execution{}, executionservice.ErrServiceUnavailable
	}
	return api.inner.Get(ctx, scope, id)
}

func ledgerPending(result executionservice.Result) executionservice.Result {
	result.Degraded = true
	result.Recoverable = true
	return result
}
