package app

import (
	"context"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

type executionReadinessSource interface {
	Readiness() executionservice.Readiness
}

type executionSnapshotSource interface {
	ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error)
}

type tradingReadinessChecker struct {
	mode                    config.Mode
	principal               string
	productionWritesEnabled bool
	execution               executionReadinessSource
	continuity              executionContinuity
	executions              executionSnapshotSource
	discoverer              boltertrader.AccountDiscoverer
}

func (checker tradingReadinessChecker) CheckTrading(ctx context.Context) readiness.TradingReadinessResult {
	mode := readiness.TradingMode(checker.mode)
	input := readiness.TradingReadinessInput{
		Mode:                            mode,
		ModeAllowsPrivateOpen:           checker.mode == config.Test,
		TestPrivateWritesAllowed:        checker.mode == config.Test,
		LiveEmergencyReduceOnlyApproved: false,
		PrivateWritesAllowed:            checker.mode == config.Test || checker.productionWritesEnabled,
	}

	if checker.execution != nil {
		status := checker.execution.Readiness()
		input.ExecutionAllowsOpen = status.Ready && !status.Degraded
		input.ExecutionAllowsClose = status.Ready && !status.Degraded
		input.RestartAfterCrash = status.Recovering
		input.HardWriteBlock = status.Degraded
	}
	if checker.continuity != nil {
		continuity := checker.continuity.Readiness()
		input.PrivateStreamFresh = continuity.Ready
		input.AuthoritativeResnapshotFresh = continuity.AuthoritativeResnapshotFresh
	}

	discovery, discoveryErr := boltertrader.AccountDiscovery{}, error(nil)
	if checker.discoverer != nil {
		discovery, discoveryErr = checker.discoverer.DiscoverAccounts(ctx)
	}
	if discoveryErr == nil {
		acceptance := boltertrader.EvaluateTwoLegWriteAcceptance(discovery)
		if acceptance.Ready {
			input.AccountFundingPaymentCapability = readiness.FundingPaymentCapabilityPresent
		}
	}

	summary := summarizeExecutionTruth(ctx, checker.executions, readmodel.Scope{
		PrincipalID: checker.principal,
		Mode:        string(checker.mode),
	}, discovery)
	input.ActiveLineage = summary.activeLineage
	input.ReduceOnlyExposureExists = summary.reduceOnlyExposure
	input.PositionScopeResolvable = summary.positionScopeResolvable
	input.WriteCapabilityPresent = summary.writeCapabilityPresent
	input.AmbiguousWriteInFlight = summary.ambiguousWriteInFlight
	input.ActiveRunLease = summary.ambiguousWriteInFlight
	input.LineageUnverifiable = summary.lineageUnverifiable
	input.ReplayOrResnapshotPossible = checker.executions != nil
	if summary.loadFailed {
		input.HardWriteBlock = true
	}

	return readiness.EvaluateTradingReadiness(input)
}

type executionTruthSummary struct {
	activeLineage           bool
	reduceOnlyExposure      bool
	positionScopeResolvable bool
	writeCapabilityPresent  bool
	ambiguousWriteInFlight  bool
	lineageUnverifiable     bool
	loadFailed              bool
}

func summarizeExecutionTruth(
	ctx context.Context,
	source executionSnapshotSource,
	scope readmodel.Scope,
	discovery boltertrader.AccountDiscovery,
) executionTruthSummary {
	if source == nil {
		return executionTruthSummary{loadFailed: true}
	}
	items, err := source.ListExecutions(ctx, scope)
	if err != nil {
		return executionTruthSummary{loadFailed: true}
	}
	closed := make(map[string]struct{})
	for _, item := range items {
		if item.Operation == execution.OperationClose && item.State == execution.StateClosed {
			positionID := strings.TrimSpace(item.PositionID)
			if positionID == "" && strings.TrimSpace(item.ParentExecutionID) != "" {
				positionID = "position-" + strings.TrimSpace(item.ParentExecutionID)
			}
			if positionID != "" {
				closed[positionID] = struct{}{}
			}
		}
	}
	accounts := make(map[string]boltertrader.DiscoveredAccount, len(discovery.Accounts))
	for _, account := range discovery.Accounts {
		accounts[account.AccountID] = account
	}
	var active []execution.Execution
	summary := executionTruthSummary{}
	for _, item := range items {
		if ambiguousExecutionState(item.State) {
			summary.ambiguousWriteInFlight = true
		}
		if item.Operation != execution.OperationOpen || !executionMayHaveExposure(item) {
			continue
		}
		positionID := "position-" + strings.TrimSpace(item.ID)
		if _, isClosed := closed[positionID]; isClosed {
			continue
		}
		active = append(active, item)
	}
	if len(active) == 0 {
		return summary
	}
	summary.activeLineage = true
	summary.reduceOnlyExposure = true
	if len(active) != 1 {
		summary.lineageUnverifiable = true
		return summary
	}
	current := active[0]
	legs, ok := exactTradingLegs(current.PlannedLegs)
	summary.positionScopeResolvable = ok
	if !ok || current.ManualIntervention || strings.TrimSpace(current.ManualInterventionReason) != "" {
		summary.lineageUnverifiable = true
	}
	if ok {
		summary.writeCapabilityPresent = true
		for _, leg := range legs {
			account, exists := accounts[leg.AccountID]
			operations := account.Capability.Operations
			if !exists || !account.WriteReady || account.Venue != leg.Venue ||
				string(account.VenueEnvironment) != leg.VenueEnvironment ||
				!operations.Place || !operations.Cancel || !operations.Positions || !operations.Fills {
				summary.writeCapabilityPresent = false
				break
			}
		}
	}
	return summary
}

func exactTradingLegs(plans []execution.PlannedLeg) ([]execution.LegIdentity, bool) {
	byRole := make(map[execution.LegRole]execution.LegIdentity, 2)
	for _, plan := range plans {
		if plan.Role != execution.LegRoleLong && plan.Role != execution.LegRoleShort {
			continue
		}
		if !plan.LegIdentity.Valid() {
			return nil, false
		}
		if _, duplicate := byRole[plan.Role]; duplicate {
			return nil, false
		}
		byRole[plan.Role] = plan.LegIdentity
	}
	long, longOK := byRole[execution.LegRoleLong]
	short, shortOK := byRole[execution.LegRoleShort]
	if !longOK || !shortOK {
		return nil, false
	}
	return []execution.LegIdentity{long, short}, true
}

func executionMayHaveExposure(item execution.Execution) bool {
	switch item.State {
	case execution.StateHedged,
		execution.StatePartiallyHedged,
		execution.StateCompensating,
		execution.StateUncertain,
		execution.StateManualInterventionRequired:
		return true
	default:
		return item.DirectionalExposure.IsPositive()
	}
}

func ambiguousExecutionState(state execution.State) bool {
	switch state {
	case execution.StateSubmitting,
		execution.StateLeg1Working,
		execution.StateLeg2Working,
		execution.StatePartiallyHedged,
		execution.StateCompensating,
		execution.StateUncertain,
		execution.StateManualInterventionRequired:
		return true
	default:
		return false
	}
}
