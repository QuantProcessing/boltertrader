package app

import (
	"context"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
)

type executionAPI interface {
	Preview(
		context.Context,
		executionservice.Scope,
		executionservice.PreviewRequest,
	) (execution.PreviewResult, error)
	Submit(
		context.Context,
		executionservice.Scope,
		executionservice.SubmitRequest,
		string,
		string,
	) (executionservice.Result, error)
	Close(
		context.Context,
		executionservice.Scope,
		executionservice.SubmitRequest,
		string,
		string,
	) (executionservice.Result, error)
	Get(
		context.Context,
		executionservice.Scope,
		string,
	) (execution.Execution, error)
}

type executionContinuity interface {
	Readiness() executionmonitor.Readiness
}

type tradingReadinessSource interface {
	CheckTrading(context.Context) readiness.TradingReadinessResult
}

type monitoredExecutionAPI struct {
	service executionAPI
	trading tradingReadinessSource
}

func (api monitoredExecutionAPI) Preview(
	ctx context.Context,
	scope executionservice.Scope,
	request executionservice.PreviewRequest,
) (execution.PreviewResult, error) {
	if api.service == nil {
		return execution.PreviewResult{}, executionservice.ErrServiceUnavailable
	}
	return api.service.Preview(ctx, scope, request)
}

func (api monitoredExecutionAPI) Submit(
	ctx context.Context,
	scope executionservice.Scope,
	request executionservice.SubmitRequest,
	idempotencyKey string,
	requestID string,
) (executionservice.Result, error) {
	request = withReceivedAt(request)
	if !api.writesReady(ctx) {
		return executionservice.Result{}, executionservice.ErrNotReady
	}
	return api.service.Submit(ctx, scope, request, idempotencyKey, requestID)
}

func (api monitoredExecutionAPI) Close(
	ctx context.Context,
	scope executionservice.Scope,
	request executionservice.SubmitRequest,
	idempotencyKey string,
	requestID string,
) (executionservice.Result, error) {
	request = withReceivedAt(request)
	if !api.closeWritesReady(ctx) {
		return executionservice.Result{}, executionservice.ErrNotReady
	}
	return api.service.Close(ctx, scope, request, idempotencyKey, requestID)
}

func (api monitoredExecutionAPI) Get(
	ctx context.Context,
	scope executionservice.Scope,
	id string,
) (execution.Execution, error) {
	if api.service == nil {
		return execution.Execution{}, executionservice.ErrServiceUnavailable
	}
	return api.service.Get(ctx, scope, id)
}

func withReceivedAt(request executionservice.SubmitRequest) executionservice.SubmitRequest {
	if request.ReceivedAt.IsZero() {
		request.ReceivedAt = time.Now().UTC()
	}
	return request
}

func (api monitoredExecutionAPI) writesReady(ctx context.Context) bool {
	return api.service != nil &&
		api.trading != nil &&
		api.trading.CheckTrading(ctx).OpenReady
}

func (api monitoredExecutionAPI) closeWritesReady(ctx context.Context) bool {
	return api.service != nil &&
		api.trading != nil &&
		api.trading.CheckTrading(ctx).CloseReady
}

type gatewayTwoLegAcceptance struct {
	discoverer boltertrader.AccountDiscoverer
}

func (gate gatewayTwoLegAcceptance) TwoLegWriteAcceptance(ctx context.Context) boltertrader.TwoLegWriteAcceptance {
	return boltertrader.CheckTwoLegWriteAcceptance(ctx, gate.discoverer)
}
