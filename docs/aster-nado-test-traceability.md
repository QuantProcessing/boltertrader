# Aster and Nado Test Traceability

The approved test spec names behavioral cases. Some are implemented as grouped
or table-driven tests, so this map is the canonical name-to-test traceability
record. The eight live write names exist exactly as specified.

| Spec case names | Implemented tests |
| --- | --- |
| `TestV3SignerDerivesExpectedAddress`, `TestV3SignerRejectsMalformedPrivateKey`, `TestV3SignerRejectsUnexpectedAddress` | `TestSecurityContextDerivesAndGuardsSigner`, `TestSecurityContextRejectsMalformedCredentials` |
| `TestV3CanonicalRequestSpotFixture`, `TestV3CanonicalRequestPerpFixture` | `TestSecurityContextMatchesIndependentEIP712Vector`, Spot/Perp `TestClientSignsV3RequestsWithoutLegacyHeader` |
| `TestNonceMonotonicWhenClockRegresses`, `TestNonceUniqueUnderConcurrency`, `TestNonceSharedAcrossProductClients` | `TestNonceCoordinatorSurvivesClockRegression`, `TestNonceCoordinatorIsMonotonicAcrossConcurrentCallers`, `TestNonceIsSharedAcrossSpotAndPerpSigning` |
| `TestTestnetProfileSelectsOnlyTestnetHosts`, `TestTestnetProfileRejectsProductionOverride` | `TestOfficialProfiles`, `TestProfileRejectsCrossEnvironmentEndpoint`, Nado `TestProfileEndpointMatrix`, `TestProfileRejectsUnknownEnvironmentAndEndpointOverride` |
| `TestReconnectAndListenKeyRefreshPreserveProfile` | `TestWsAccountClientReconnectsWithRenewedListenKey`, `TestPerpWsAccountClientReconnectsOnListenKeyExpiredAndDeletesFinalKey`, Nado reconnect/profile tests in `ws_lifecycle_test.go` |
| `TestSensitiveValuesAreRedacted`, `TestAmbiguousWriteRequiresStatusReconciliation` | `TestSecurityDiagnosticsAreRedacted`, typed/redacted error tests, Spot/Perp `TestPlaceOrderDoesNotRetryAmbiguousTransportFailure` |
| `TestAsterSpotTestnetSymbolRejectsTrimmedCaseInsensitiveTESTPrefix`, `TestAsterPerpTestnetSymbolRejectsTrimmedCaseInsensitiveTESTPrefix` | `TestNormalizeSymbolRejectsTestPrefixForEveryTestnetProduct`, Spot/Perp client and WS rejection tests |
| `TestAsterSpotDiscoveryFiltersTESTSymbols`, `TestAsterPerpDiscoveryFiltersTESTSymbols`, `TestAsterDiscoveryFailsWhenOnlyTESTSymbolsRemain` | `TestFilterDiscoverySymbolsDropsTestCandidatesForEveryTestnetProduct`, `TestFilterDiscoverySymbolsFailsWhenNoSafeCandidateRemains` |
| `TestRiskCheckRequiresContextWhenVenueValidationRequired`, `TestRiskCheckContextUsesVenueValidatorForConfiguredKind`, `TestLocalLimitsRejectBeforeVenuePreTradeQuery`, `TestNadoCheckContextFailsClosedOnStaleAccountBeforeVenueValidation` | `TestConfiguredVenueValidatorRequiresContextCheck`, `TestVenueValidatorRunsAfterLocalChecksAndReplacesBalanceClaim`, `TestVenueValidatorRejectsStaleAccountBeforeVenueIO` |
| `TestContextRiskCheckerReturnsPreTradeLease`, `TestPreTradeLeaseReleaseIsIdempotentAndConcurrentSafe`, `TestPreTradeLeaseReleaseAfterConsumeIsNoOp`, `TestPreTradeLeaseNeverLogsClientIDPayloadOrSignature` | `TestSuccessfulVenueValidatorTransfersLeaseOwnership`, `TestNadoPreparedLeaseReleaseIsConcurrentSafeAndRemovesPayload`, `TestNadoPreTradeSafetyReviewedBlockers` |
| `TestVenueCapacityRejectionDoesNotTouchJournalCacheSignOrSubmit` | `TestNadoPreTradeRejectsBeforePrepareAndExecute`, `TestNadoPreTradeSafetyReviewedBlockers` |
| `TestNadoAccountSummaryMapsHealths0AndHealths2WithoutFreeBalance` | `TestNadoSDKFixturesBackAdapterDiscoveryAndAccountMapping`, `TestNadoAccountFixturePreservesHealthAndSignedLiabilities` |
| `TestNadoSpotPreparedPayloadForcesSpotLeverageFalse`, `TestNadoPerpPreparedPayloadPreservesReduceOnly`, `TestNadoPreparedPayloadConsumedExactlyOnce` | `TestPrepareNadoOrderInputSafetyEnvelope`, `TestPreparedOrderCarriesSpotLeverageFalseAndExecutesExactRequest`, `TestNadoPreTradeValidatesReadOnlyBeforePreparedLeaseAndSubmitConsumesOnce` |
| `TestNadoPreparedPayloadExpiresAndRedactsSecrets`, `TestNadoPreparedPayloadReleasedOnContextCancelBeforeJournal`, `TestNadoPreparedPayloadReleasedOnContextCancelDuringJournal`, `TestNadoPreparedPayloadReleasedOnJournalFailure` | `TestNadoRuntimeExpiredPreparedPayloadClosesLocalDeniedWithoutRevalidation`, cancellation/release tests in `runtime/exec/pretrade_test.go`, `TestNadoRuntimeJournalFailureReleasesPreparedPayloadBeforeVenueWrite` |
| `TestNadoPostIntentPreSubmitCancelClosesLocalDeniedWithoutPendingOrder`, `TestNadoDirectSubmitFallbackCleansEveryAbortStage` | `TestSubmitCancellationAfterIntentClosesJournalWithoutPendingOrder`, `TestNadoSubmitFailsClosedUntilPreparedValidationExists`, `TestNadoPreTradeCancellationStagesStopBeforeLaterVenueCalls` |

Run the mapped offline surface with:

```sh
make test
make test-p6-offline
go test -race ./sdk/aster/common/... ./adapter/aster/... ./adapter/nado/... -count=1
```

## Live Testnet Verdict

Fresh serial evidence from 2026-07-13:

| Row | Verdict | Evidence |
| --- | --- | --- |
| Aster Spot adapter | PASS | GTX place/cancel, IOC buy/sell, private execution/account events, no open orders, bounded base-asset delta |
| Aster Spot runtime | PASS | runtime-local risk rejection before venue handoff, bus/cache/account/portfolio readiness, reconcile, bounded base-asset delta |
| Aster Perp adapter | PASS | GTX place/cancel, IOC fill, reduce-only close, private events, flat venue position |
| Aster Perp runtime | PASS | runtime-local risk rejection before venue handoff, cache lifecycle, portfolio flat, final reconcile |
| Nado Spot adapter | PASS | funded-only/no-borrow, GTX place/cancel, IOC buy/sell, private fill evidence, no open orders, prepared entries zero |
| Nado Spot runtime | PASS | local risk rejection before venue I/O, cache cancel/fill lifecycle, reconcile, no open orders, prepared entries zero |
| Nado Perp adapter | PASS | discovered isolated-only mode, 1x opening margin, GTX place/cancel, IOC fill, reduce-only close, flat position |
| Nado Perp runtime | PASS | local risk rejection, isolated prepared payload, cache lifecycle, reconcile, no open orders, flat position |

Nado's currently tradable Testnet products exceed the default `100 USDT0`
notional cap, so the accepted rows use the explicit `110 USDT0` override. The
Testnet server acknowledged wildcard `order_update` subscriptions but emitted
no order events during these rows. Acceptance therefore requires a private fill
event and independently proves order state through command responses, REST open
orders, archive fills, runtime cache transitions, and final reconciliation.
