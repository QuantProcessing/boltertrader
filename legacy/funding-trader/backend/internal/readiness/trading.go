package readiness

type TradingMode string

const (
	TradingModeTest TradingMode = "test"
	TradingModeLive TradingMode = "live"
)

type FundingPaymentCapability string

const (
	FundingPaymentCapabilityUnsupported FundingPaymentCapability = "unsupported"
	FundingPaymentCapabilityPresent     FundingPaymentCapability = "present"
)

type TradingReadinessReason string

const (
	TradingReadinessReasonNormalTestOpenPath                   TradingReadinessReason = "normal_test_open_path"
	TradingReadinessReasonNormalTestCloseRecovery              TradingReadinessReason = "normal_test_close_recovery"
	TradingReadinessReasonAuthoritativeResnapshotCloseRecovery TradingReadinessReason = "authoritative_resnapshot_close_recovery"
	TradingReadinessReasonManualBlockedOrUnavailable           TradingReadinessReason = "manual_blocked_or_unavailable"
	TradingReadinessReasonLineageUnverifiableUntilRepaired     TradingReadinessReason = "lineage_unverifiable_until_repaired"
	TradingReadinessReasonLivePrivateWritesDenied              TradingReadinessReason = "live_private_writes_denied"
	TradingReadinessReasonLiveReadOnlyRecoveryOnly             TradingReadinessReason = "live_read_only_recovery_only"
	TradingReadinessReasonEmergencyReduceOnlyRecovery          TradingReadinessReason = "emergency_reduce_only_recovery"
	TradingReadinessReasonNotReady                             TradingReadinessReason = "not_ready"
)

type TradingReadinessInput struct {
	Mode                            TradingMode
	ModeAllowsPrivateOpen           bool
	TestPrivateWritesAllowed        bool
	LiveEmergencyReduceOnlyApproved bool

	PrivateStreamFresh           bool
	AuthoritativeResnapshotFresh bool

	ExecutionAllowsOpen  bool
	ExecutionAllowsClose bool

	AccountFundingPaymentCapability FundingPaymentCapability
	WriteCapabilityPresent          bool
	PrivateWritesAllowed            bool

	ActiveLineage            bool
	ActiveRunLease           bool
	LineageUnverifiable      bool
	ReduceOnlyExposureExists bool
	RestartAfterCrash        bool

	PositionScopeResolvable    bool
	ReplayOrResnapshotPossible bool
	AmbiguousWriteInFlight     bool
	HardWriteBlock             bool
}

type TradingReadinessResult struct {
	OpenReady     bool
	CloseReady    bool
	RecoveryReady bool
	Reason        TradingReadinessReason
}

func EvaluateTradingReadiness(in TradingReadinessInput) TradingReadinessResult {
	authoritativeStateFresh := in.PrivateStreamFresh || in.AuthoritativeResnapshotFresh
	modeAllowsReduceOnlyClose := (in.Mode == TradingModeTest && in.TestPrivateWritesAllowed) ||
		(in.Mode == TradingModeLive && in.LiveEmergencyReduceOnlyApproved)

	openReady := in.Mode == TradingModeTest &&
		in.ModeAllowsPrivateOpen &&
		in.PrivateStreamFresh &&
		in.ExecutionAllowsOpen &&
		fundingPaymentCapabilitySupported(in.AccountFundingPaymentCapability) &&
		!in.ActiveLineage &&
		!in.ActiveRunLease &&
		!in.LineageUnverifiable &&
		in.PrivateWritesAllowed

	closeReady := in.PositionScopeResolvable &&
		authoritativeStateFresh &&
		!in.LineageUnverifiable &&
		in.ExecutionAllowsClose &&
		modeAllowsReduceOnlyClose &&
		in.WriteCapabilityPresent &&
		(in.ActiveLineage || in.ReduceOnlyExposureExists) &&
		!in.AmbiguousWriteInFlight &&
		!in.HardWriteBlock

	recoveryReady := in.PositionScopeResolvable &&
		authoritativeStateFresh &&
		in.ReplayOrResnapshotPossible &&
		(in.ActiveLineage ||
			in.LineageUnverifiable ||
			in.RestartAfterCrash ||
			in.ReduceOnlyExposureExists)

	return TradingReadinessResult{
		OpenReady:     openReady,
		CloseReady:    closeReady,
		RecoveryReady: recoveryReady,
		Reason:        tradingReadinessReason(in, openReady, closeReady, recoveryReady, authoritativeStateFresh),
	}
}

func fundingPaymentCapabilitySupported(capability FundingPaymentCapability) bool {
	return capability != "" && capability != FundingPaymentCapabilityUnsupported
}

func tradingReadinessReason(
	in TradingReadinessInput,
	openReady bool,
	closeReady bool,
	recoveryReady bool,
	authoritativeStateFresh bool,
) TradingReadinessReason {
	if openReady {
		return TradingReadinessReasonNormalTestOpenPath
	}
	if in.Mode == TradingModeLive && in.ReduceOnlyExposureExists && closeReady {
		return TradingReadinessReasonEmergencyReduceOnlyRecovery
	}
	if in.Mode == TradingModeLive && in.ReduceOnlyExposureExists && recoveryReady {
		return TradingReadinessReasonLiveReadOnlyRecoveryOnly
	}
	if in.Mode == TradingModeLive && !in.ReduceOnlyExposureExists {
		return TradingReadinessReasonLivePrivateWritesDenied
	}
	if in.LineageUnverifiable && recoveryReady {
		return TradingReadinessReasonLineageUnverifiableUntilRepaired
	}
	if closeReady && recoveryReady && in.Mode == TradingModeTest {
		if in.PrivateStreamFresh {
			return TradingReadinessReasonNormalTestCloseRecovery
		}
		if in.AuthoritativeResnapshotFresh {
			return TradingReadinessReasonAuthoritativeResnapshotCloseRecovery
		}
	}
	if !authoritativeStateFresh {
		return TradingReadinessReasonManualBlockedOrUnavailable
	}
	return TradingReadinessReasonNotReady
}
