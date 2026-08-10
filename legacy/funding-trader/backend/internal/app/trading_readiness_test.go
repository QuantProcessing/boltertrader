package app

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestAppTradingReadinessSeparatesBlockedOpenFromNormalClose(t *testing.T) {
	open := activeOpenExecution()
	checker := tradingReadinessChecker{
		mode:       config.Test,
		principal:  "principal-default",
		execution:  staticExecutionReadiness{status: executionservice.Readiness{Ready: true}},
		continuity: fakeExecutionContinuity{status: executionmonitor.Readiness{Ready: true}},
		executions: staticExecutionSnapshots{items: []execution.Execution{open}},
		discoverer: &fakeAppAccountDiscoverer{discovery: boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
			fakeReadyExecutionAccount("account-long", "binance"),
			func() boltertrader.DiscoveredAccount {
				account := fakeReadyExecutionAccount("account-short", "lighter")
				account.VenueEnvironment = boltertrader.GatewayTestnet
				return account
			}(),
		}}},
	}

	got := checker.CheckTrading(context.Background())
	if got.OpenReady || !got.CloseReady || !got.RecoveryReady {
		t.Fatalf("trading readiness=%+v, want open=false close=true recovery=true", got)
	}
	if got.Reason != readiness.TradingReadinessReasonNormalTestCloseRecovery {
		t.Fatalf("reason=%q", got.Reason)
	}
}

func TestAppTradingReadinessFailsClosedOnUnresolvableLineage(t *testing.T) {
	open := activeOpenExecution()
	open.ManualIntervention = true
	open.ManualInterventionReason = "lineage drift"
	checker := tradingReadinessChecker{
		mode:       config.Test,
		principal:  "principal-default",
		execution:  staticExecutionReadiness{status: executionservice.Readiness{Ready: true}},
		continuity: fakeExecutionContinuity{status: executionmonitor.Readiness{Ready: true}},
		executions: staticExecutionSnapshots{items: []execution.Execution{open}},
		discoverer: &fakeAppAccountDiscoverer{discovery: boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
			fakeReadyExecutionAccount("account-long", "binance"),
			fakeReadyExecutionAccount("account-short", "lighter"),
		}}},
	}

	got := checker.CheckTrading(context.Background())
	if got.OpenReady || got.CloseReady || !got.RecoveryReady {
		t.Fatalf("trading readiness=%+v, want read-only recovery", got)
	}
	if got.Reason != readiness.TradingReadinessReasonLineageUnverifiableUntilRepaired {
		t.Fatalf("reason=%q", got.Reason)
	}
}

func TestAppTradingReadinessAllowsTestCloseAfterAuthoritativeResnapshotWhileStreamReconnects(t *testing.T) {
	checker := tradingReadinessChecker{
		mode:      config.Test,
		principal: "principal-default",
		execution: staticExecutionReadiness{status: executionservice.Readiness{Ready: true}},
		continuity: fakeExecutionContinuity{status: executionmonitor.Readiness{
			Ready: false, AuthoritativeResnapshotFresh: true, Reason: "not-subscribed",
		}},
		executions: staticExecutionSnapshots{items: []execution.Execution{activeOpenExecution()}},
		discoverer: &fakeAppAccountDiscoverer{discovery: boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
			fakeReadyExecutionAccount("account-long", "binance"),
			func() boltertrader.DiscoveredAccount {
				account := fakeReadyExecutionAccount("account-short", "lighter")
				account.VenueEnvironment = boltertrader.GatewayTestnet
				return account
			}(),
		}}},
	}

	got := checker.CheckTrading(context.Background())
	if got.OpenReady || !got.CloseReady || !got.RecoveryReady || got.Reason != readiness.TradingReadinessReasonAuthoritativeResnapshotCloseRecovery {
		t.Fatalf("authoritative resnapshot readiness=%+v", got)
	}
}

func TestAppTradingReadinessLiveHasNoImplicitEmergencyApproval(t *testing.T) {
	checker := tradingReadinessChecker{
		mode:       config.Live,
		principal:  "principal-default",
		execution:  staticExecutionReadiness{status: executionservice.Readiness{Ready: true}},
		continuity: fakeExecutionContinuity{status: executionmonitor.Readiness{Ready: true}},
		executions: staticExecutionSnapshots{items: []execution.Execution{activeOpenExecution()}},
		discoverer: &fakeAppAccountDiscoverer{discovery: boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
			fakeReadyExecutionAccount("account-long", "binance"),
			fakeReadyExecutionAccount("account-short", "lighter"),
		}}},
	}

	got := checker.CheckTrading(context.Background())
	if got.OpenReady || got.CloseReady || !got.RecoveryReady || got.Reason != readiness.TradingReadinessReasonLiveReadOnlyRecoveryOnly {
		t.Fatalf("live trading readiness=%+v", got)
	}
}

func TestAppTradingReadinessKeepsRecoveryReadyWhenExecutionWritesAreDegraded(t *testing.T) {
	checker := tradingReadinessChecker{
		mode:      config.Test,
		principal: "principal-default",
		execution: staticExecutionReadiness{status: executionservice.Readiness{
			Ready: false, Degraded: true, Recovering: true,
		}},
		continuity: fakeExecutionContinuity{status: executionmonitor.Readiness{Ready: true}},
		executions: staticExecutionSnapshots{items: []execution.Execution{activeOpenExecution()}},
		discoverer: &fakeAppAccountDiscoverer{discovery: boltertrader.AccountDiscovery{Accounts: []boltertrader.DiscoveredAccount{
			fakeReadyExecutionAccount("account-long", "binance"),
			func() boltertrader.DiscoveredAccount {
				account := fakeReadyExecutionAccount("account-short", "lighter")
				account.VenueEnvironment = boltertrader.GatewayTestnet
				return account
			}(),
		}}},
	}

	got := checker.CheckTrading(context.Background())
	if got.OpenReady || got.CloseReady || !got.RecoveryReady {
		t.Fatalf("trading readiness=%+v, want open=false close=false recovery=true", got)
	}
}

type staticExecutionReadiness struct {
	status executionservice.Readiness
}

func (source staticExecutionReadiness) Readiness() executionservice.Readiness {
	return source.status
}

type staticExecutionSnapshots struct {
	items []execution.Execution
	err   error
}

func (source staticExecutionSnapshots) ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error) {
	return append([]execution.Execution(nil), source.items...), source.err
}

func activeOpenExecution() execution.Execution {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	return execution.Execution{
		ID: "open-1", PrincipalID: "principal-default", Mode: "test",
		Operation: execution.OperationOpen, State: execution.StateHedged,
		LineageID: "open-1", CreatedAt: now, UpdatedAt: now,
		PlannedLegs: []execution.PlannedLeg{
			{LegIdentity: execution.LegIdentity{AccountID: "account-long", Venue: "binance", InstrumentID: "BTC-USDT-PERP", VenueEnvironment: "demo"}, Role: execution.LegRoleLong},
			{LegIdentity: execution.LegIdentity{AccountID: "account-short", Venue: "lighter", InstrumentID: "BTC-USDT-PERP", VenueEnvironment: "testnet"}, Role: execution.LegRoleShort},
		},
	}
}
