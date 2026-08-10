package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
)

func TestMonitoredExecutionAPIFailsClosedForWritesButAllowsPreviewAndReads(t *testing.T) {
	service := &fakeExecutionAPI{}
	api := monitoredExecutionAPI{
		service: service,
		trading: fakeTradingReadiness{status: readiness.TradingReadinessResult{CloseReady: false}},
	}
	scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

	if _, err := api.Preview(context.Background(), scope, executionservice.PreviewRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Get(context.Background(), scope, "execution-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Submit(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); !errors.Is(err, executionservice.ErrNotReady) {
		t.Fatalf("unready submit err=%v", err)
	}
	if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); !errors.Is(err, executionservice.ErrNotReady) {
		t.Fatalf("unready close err=%v", err)
	}
	if service.previewCalls != 1 || service.getCalls != 1 || service.submitCalls != 0 || service.closeCalls != 0 {
		t.Fatalf("calls=%+v", service)
	}
}

func TestMonitoredExecutionCloseAllowsAuthoritativeResnapshotReadiness(t *testing.T) {
	service := &fakeExecutionAPI{}
	api := monitoredExecutionAPI{
		service: service,
		trading: fakeTradingReadiness{status: readiness.TradingReadinessResult{
			OpenReady:     false,
			CloseReady:    true,
			RecoveryReady: true,
			Reason:        readiness.TradingReadinessReasonAuthoritativeResnapshotCloseRecovery,
		}},
	}
	scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

	if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); err != nil {
		t.Fatalf("authoritative resnapshot close err=%v", err)
	}
	if service.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1", service.closeCalls)
	}
}

func TestMonitoredExecutionOpenFailsClosedButNormalCloseDoesNotRequireNewTwoLegAcceptance(t *testing.T) {
	service := &fakeExecutionAPI{}
	api := monitoredExecutionAPI{
		service: service,
		trading: fakeTradingReadiness{status: readiness.TradingReadinessResult{
			OpenReady: false, CloseReady: true,
		}},
	}
	scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

	if _, err := api.Submit(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); !errors.Is(err, executionservice.ErrNotReady) {
		t.Fatalf("blocked submit err=%v", err)
	}
	if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); err != nil {
		t.Fatalf("normal reduce-only close err=%v", err)
	}
	if service.submitCalls != 0 || service.closeCalls != 1 {
		t.Fatalf("service=%+v", service)
	}
}

func TestMonitoredExecutionAPIDelegatesWritesOnlyWhenOperationReadinessIsReady(t *testing.T) {
	service := &fakeExecutionAPI{}
	api := monitoredExecutionAPI{
		service: service,
		trading: fakeTradingReadiness{status: readiness.TradingReadinessResult{
			OpenReady: true, CloseReady: true,
		}},
	}
	scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

	if _, err := api.Submit(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); err != nil {
		t.Fatal(err)
	}
	if service.submitCalls != 1 || service.closeCalls != 1 {
		t.Fatalf("write calls=%+v", service)
	}
}

func TestMonitoredExecutionAPIStampsMissingReceiptTimeBeforeReadiness(t *testing.T) {
	service := &fakeExecutionAPI{}
	api := monitoredExecutionAPI{
		service: service,
		trading: fakeTradingReadiness{status: readiness.TradingReadinessResult{
			OpenReady: true, CloseReady: true,
		}},
	}
	scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

	startedAt := time.Now().UTC()
	if _, err := api.Submit(context.Background(), scope, executionservice.SubmitRequest{}, "key-open", "request-open"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "key-close", "request-close"); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC()

	for operation, receivedAt := range map[string]time.Time{
		"submit": service.submitRequest.ReceivedAt,
		"close":  service.closeRequest.ReceivedAt,
	} {
		if receivedAt.Before(startedAt) || receivedAt.After(finishedAt) {
			t.Fatalf("%s receivedAt = %s, want within [%s, %s]", operation, receivedAt, startedAt, finishedAt)
		}
	}
	preservedAt := startedAt.Add(-time.Minute)
	if _, err := api.Submit(context.Background(), scope, executionservice.SubmitRequest{ReceivedAt: preservedAt}, "key-preserve", "request-preserve"); err != nil {
		t.Fatal(err)
	}
	if !service.submitRequest.ReceivedAt.Equal(preservedAt) {
		t.Fatalf("receivedAt = %s, want existing boundary timestamp %s", service.submitRequest.ReceivedAt, preservedAt)
	}
}

func TestMonitoredExecutionCloseRequiresOperationSpecificCloseReadiness(t *testing.T) {
	tests := []struct {
		name   string
		status readiness.TradingReadinessResult
	}{
		{
			name: "live emergency close not approved",
			status: readiness.TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    false,
				RecoveryReady: true,
				Reason:        readiness.TradingReadinessReasonLiveReadOnlyRecoveryOnly,
			},
		},
		{
			name: "test close not ready",
			status: readiness.TradingReadinessResult{
				OpenReady:     true,
				CloseReady:    false,
				RecoveryReady: true,
				Reason:        readiness.TradingReadinessReasonNormalTestOpenPath,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeExecutionAPI{}
			api := monitoredExecutionAPI{
				service: service,
				trading: fakeTradingReadiness{status: tt.status},
			}
			scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

			if _, err := api.Close(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); !errors.Is(err, executionservice.ErrNotReady) {
				t.Fatalf("close err=%v", err)
			}
			if service.closeCalls != 0 {
				t.Fatalf("close delegated with readiness=%+v", tt.status)
			}
		})
	}
}

func TestMonitoredExecutionSubmitRequiresOperationSpecificOpenReadiness(t *testing.T) {
	service := &fakeExecutionAPI{}
	api := monitoredExecutionAPI{
		service: service,
		trading: fakeTradingReadiness{status: readiness.TradingReadinessResult{
			OpenReady: false, CloseReady: true, RecoveryReady: true,
			Reason: readiness.TradingReadinessReasonNormalTestCloseRecovery,
		}},
	}
	scope := executionservice.Scope{PrincipalID: "principal-default", Mode: "test"}

	if _, err := api.Submit(context.Background(), scope, executionservice.SubmitRequest{}, "key", "request"); !errors.Is(err, executionservice.ErrNotReady) {
		t.Fatalf("submit err=%v, want not ready", err)
	}
	if service.submitCalls != 0 {
		t.Fatalf("submit delegated while openReady=false: calls=%d", service.submitCalls)
	}
}

func TestGatewayTwoLegAcceptanceUsesLiveDiscovererForEveryCheck(t *testing.T) {
	discoverer := &fakeAppAccountDiscoverer{
		discovery: boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
			fakeReadyExecutionAccount("binance-perp", "binance"),
			fakeReadyExecutionAccount("lighter-perp", "lighter"),
		}},
	}
	gate := gatewayTwoLegAcceptance{discoverer: discoverer}
	if got := gate.TwoLegWriteAcceptance(context.Background()); !got.Ready {
		t.Fatalf("first acceptance=%+v", got)
	}
	discoverer.discovery = boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
		fakeReadyExecutionAccount("binance-perp", "binance"),
	}}
	if got := gate.TwoLegWriteAcceptance(context.Background()); got.Ready ||
		got.Reason != boltertrader.TwoLegWriteReadyReasonInsufficientPerpVenues {
		t.Fatalf("second acceptance=%+v", got)
	}
	if discoverer.calls != 2 {
		t.Fatalf("discover calls=%d", discoverer.calls)
	}
}

type fakeExecutionContinuity struct {
	status executionmonitor.Readiness
}

func (continuity fakeExecutionContinuity) Readiness() executionmonitor.Readiness {
	return continuity.status
}

type fakeTradingReadiness struct {
	status readiness.TradingReadinessResult
}

func (source fakeTradingReadiness) CheckTrading(context.Context) readiness.TradingReadinessResult {
	return source.status
}

type fakeExecutionAPI struct {
	previewCalls  int
	submitCalls   int
	closeCalls    int
	getCalls      int
	submitRequest executionservice.SubmitRequest
	closeRequest  executionservice.SubmitRequest
}

func (service *fakeExecutionAPI) Preview(
	context.Context,
	executionservice.Scope,
	executionservice.PreviewRequest,
) (execution.PreviewResult, error) {
	service.previewCalls++
	return execution.PreviewResult{}, nil
}

func (service *fakeExecutionAPI) Submit(
	_ context.Context,
	_ executionservice.Scope,
	request executionservice.SubmitRequest,
	_ string,
	_ string,
) (executionservice.Result, error) {
	service.submitCalls++
	service.submitRequest = request
	return executionservice.Result{}, nil
}

func (service *fakeExecutionAPI) Close(
	_ context.Context,
	_ executionservice.Scope,
	request executionservice.SubmitRequest,
	_ string,
	_ string,
) (executionservice.Result, error) {
	service.closeCalls++
	service.closeRequest = request
	return executionservice.Result{}, nil
}

func (service *fakeExecutionAPI) Get(
	context.Context,
	executionservice.Scope,
	string,
) (execution.Execution, error) {
	service.getCalls++
	return execution.Execution{}, nil
}

type fakeAppAccountDiscoverer struct {
	discovery boltertrader.AccountDiscovery
	calls     int
}

func (discoverer *fakeAppAccountDiscoverer) DiscoverAccounts(context.Context) (boltertrader.AccountDiscovery, error) {
	discoverer.calls++
	return discoverer.discovery, nil
}

func fakeReadyExecutionAccount(accountID, venue string) boltertrader.DiscoveredAccount {
	return boltertrader.DiscoveredAccount{
		AccountID: accountID, Venue: venue, Product: "perp",
		VenueEnvironment: boltertrader.GatewayDemo, Settlement: "USDT",
		ReadReady: true, WriteReady: true,
		Capability: boltertrader.CapabilityRow{Operations: boltertrader.CapabilityOperations{
			Place: true, Cancel: true, OpenOrders: true, OrderHistory: true,
			Fills: true, Balances: true, Account: true, Positions: true,
			Funding: true, AccountFundingPayments: true, PrivateStream: true,
		}},
	}
}
