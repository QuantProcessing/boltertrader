package readiness

import "testing"

func TestReadinessTruthTableNoMaybe(t *testing.T) {
	tests := []struct {
		name string
		in   TradingReadinessInput
		want TradingReadinessResult
	}{
		{
			name: "normal Test open path",
			in: TradingReadinessInput{
				Mode:                            TradingModeTest,
				ModeAllowsPrivateOpen:           true,
				PrivateStreamFresh:              true,
				ExecutionAllowsOpen:             true,
				AccountFundingPaymentCapability: FundingPaymentCapabilityPresent,
				PrivateWritesAllowed:            true,
				PositionScopeResolvable:         true,
				ReplayOrResnapshotPossible:      true,
			},
			want: TradingReadinessResult{
				OpenReady:     true,
				CloseReady:    false,
				RecoveryReady: false,
				Reason:        TradingReadinessReasonNormalTestOpenPath,
			},
		},
		{
			name: "normal Test reduce-only close and recovery path",
			in: TradingReadinessInput{
				Mode:                            TradingModeTest,
				TestPrivateWritesAllowed:        true,
				PrivateStreamFresh:              true,
				ExecutionAllowsClose:            true,
				AccountFundingPaymentCapability: FundingPaymentCapabilityPresent,
				ActiveLineage:                   true,
				PositionScopeResolvable:         true,
				WriteCapabilityPresent:          true,
				ReplayOrResnapshotPossible:      true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    true,
				RecoveryReady: true,
				Reason:        TradingReadinessReasonNormalTestCloseRecovery,
			},
		},
		{
			name: "authoritative Test resnapshot close and recovery",
			in: TradingReadinessInput{
				Mode:                            TradingModeTest,
				TestPrivateWritesAllowed:        true,
				AuthoritativeResnapshotFresh:    true,
				ExecutionAllowsClose:            true,
				AccountFundingPaymentCapability: FundingPaymentCapabilityPresent,
				ActiveLineage:                   true,
				PositionScopeResolvable:         true,
				WriteCapabilityPresent:          true,
				ReplayOrResnapshotPossible:      true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    true,
				RecoveryReady: true,
				Reason:        TradingReadinessReasonAuthoritativeResnapshotCloseRecovery,
			},
		},
		{
			name: "manual blocked or unavailable",
			in: TradingReadinessInput{
				Mode:                            TradingModeTest,
				TestPrivateWritesAllowed:        true,
				ExecutionAllowsClose:            true,
				AccountFundingPaymentCapability: FundingPaymentCapabilityPresent,
				ActiveLineage:                   true,
				PositionScopeResolvable:         true,
				WriteCapabilityPresent:          true,
				ReplayOrResnapshotPossible:      true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    false,
				RecoveryReady: false,
				Reason:        TradingReadinessReasonManualBlockedOrUnavailable,
			},
		},
		{
			name: "lineage drifted but repair possible",
			in: TradingReadinessInput{
				Mode:                       TradingModeTest,
				PrivateStreamFresh:         true,
				LineageUnverifiable:        true,
				ActiveLineage:              true,
				PositionScopeResolvable:    true,
				ReplayOrResnapshotPossible: true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    false,
				RecoveryReady: true,
				Reason:        TradingReadinessReasonLineageUnverifiableUntilRepaired,
			},
		},
		{
			name: "Live default denied with no exposure",
			in: TradingReadinessInput{
				Mode:                    TradingModeLive,
				PrivateStreamFresh:      true,
				PositionScopeResolvable: true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    false,
				RecoveryReady: false,
				Reason:        TradingReadinessReasonLivePrivateWritesDenied,
			},
		},
		{
			name: "Live exposure without emergency approval is read-only recovery",
			in: TradingReadinessInput{
				Mode:                       TradingModeLive,
				PrivateStreamFresh:         true,
				PositionScopeResolvable:    true,
				ReplayOrResnapshotPossible: true,
				ReduceOnlyExposureExists:   true,
				ExecutionAllowsClose:       true,
				WriteCapabilityPresent:     true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    false,
				RecoveryReady: true,
				Reason:        TradingReadinessReasonLiveReadOnlyRecoveryOnly,
			},
		},
		{
			name: "Live exposure with emergency reduce-only approval",
			in: TradingReadinessInput{
				Mode:                            TradingModeLive,
				LiveEmergencyReduceOnlyApproved: true,
				PrivateStreamFresh:              true,
				PositionScopeResolvable:         true,
				ReplayOrResnapshotPossible:      true,
				ReduceOnlyExposureExists:        true,
				ExecutionAllowsClose:            true,
				WriteCapabilityPresent:          true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    true,
				RecoveryReady: true,
				Reason:        TradingReadinessReasonEmergencyReduceOnlyRecovery,
			},
		},
		{
			name: "open capability unknown fails closed",
			in: TradingReadinessInput{
				Mode:                  TradingModeTest,
				ModeAllowsPrivateOpen: true,
				PrivateStreamFresh:    true,
				ExecutionAllowsOpen:   true,
				PrivateWritesAllowed:  true,
			},
			want: TradingReadinessResult{
				OpenReady:     false,
				CloseReady:    false,
				RecoveryReady: false,
				Reason:        TradingReadinessReasonNotReady,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateTradingReadiness(tt.in)
			if got != tt.want {
				t.Fatalf("EvaluateTradingReadiness() = %+v, want %+v", got, tt.want)
			}
			if got.Reason == "" {
				t.Fatal("reason must be a stable machine reason, not an empty or maybe state")
			}
		})
	}
}

func TestReadinessReadOnlyRecoveryForUnverifiableLineage(t *testing.T) {
	got := EvaluateTradingReadiness(TradingReadinessInput{
		Mode:                       TradingModeTest,
		ModeAllowsPrivateOpen:      true,
		TestPrivateWritesAllowed:   true,
		PrivateStreamFresh:         true,
		ExecutionAllowsOpen:        true,
		ExecutionAllowsClose:       true,
		ActiveLineage:              true,
		LineageUnverifiable:        true,
		PrivateWritesAllowed:       true,
		PositionScopeResolvable:    true,
		WriteCapabilityPresent:     true,
		ReplayOrResnapshotPossible: true,
	})
	if got.OpenReady || got.CloseReady || !got.RecoveryReady {
		t.Fatalf("unverifiable lineage readiness = %+v, want open=false close=false recovery=true", got)
	}
	if got.Reason != TradingReadinessReasonLineageUnverifiableUntilRepaired {
		t.Fatalf("reason = %q, want %q", got.Reason, TradingReadinessReasonLineageUnverifiableUntilRepaired)
	}
}

func TestReadinessNormalCloseAndResnapshotRecovery(t *testing.T) {
	got := EvaluateTradingReadiness(TradingReadinessInput{
		Mode:                         TradingModeTest,
		TestPrivateWritesAllowed:     true,
		AuthoritativeResnapshotFresh: true,
		ExecutionAllowsClose:         true,
		ActiveLineage:                true,
		PositionScopeResolvable:      true,
		WriteCapabilityPresent:       true,
		ReplayOrResnapshotPossible:   true,
	})
	if got.OpenReady || !got.CloseReady || !got.RecoveryReady {
		t.Fatalf("resnapshot readiness = %+v, want open=false close=true recovery=true", got)
	}
	if got.Reason != TradingReadinessReasonAuthoritativeResnapshotCloseRecovery {
		t.Fatalf("reason = %q, want %q", got.Reason, TradingReadinessReasonAuthoritativeResnapshotCloseRecovery)
	}
}

func TestReadinessLiveEmergencyReduceOnly(t *testing.T) {
	base := TradingReadinessInput{
		Mode:                       TradingModeLive,
		PrivateStreamFresh:         true,
		ExecutionAllowsClose:       true,
		ReduceOnlyExposureExists:   true,
		PositionScopeResolvable:    true,
		WriteCapabilityPresent:     true,
		ReplayOrResnapshotPossible: true,
	}

	denied := EvaluateTradingReadiness(base)
	if denied.OpenReady || denied.CloseReady || !denied.RecoveryReady {
		t.Fatalf("live default readiness = %+v, want open=false close=false recovery=true", denied)
	}
	if denied.Reason != TradingReadinessReasonLiveReadOnlyRecoveryOnly {
		t.Fatalf("denied reason = %q, want %q", denied.Reason, TradingReadinessReasonLiveReadOnlyRecoveryOnly)
	}

	base.LiveEmergencyReduceOnlyApproved = true
	approved := EvaluateTradingReadiness(base)
	if approved.OpenReady || !approved.CloseReady || !approved.RecoveryReady {
		t.Fatalf("live emergency readiness = %+v, want open=false close=true recovery=true", approved)
	}
	if approved.Reason != TradingReadinessReasonEmergencyReduceOnlyRecovery {
		t.Fatalf("approved reason = %q, want %q", approved.Reason, TradingReadinessReasonEmergencyReduceOnlyRecovery)
	}
}
