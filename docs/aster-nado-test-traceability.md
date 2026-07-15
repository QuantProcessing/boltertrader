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
| `TestSensitiveValuesAreRedacted`, `TestAmbiguousWriteRequiresStatusReconciliation` | `TestSecurityDiagnosticsAreRedacted`, typed/redacted error tests, `TestSpotPlaceOrderDoesNotRetryAmbiguousTransportFailure`, `TestPerpPlaceOrderDoesNotRetryAmbiguousTransportFailure` |
| `TestAsterSpotTestnetSymbolRejectsTrimmedCaseInsensitiveTESTPrefix`, `TestAsterPerpTestnetSymbolRejectsTrimmedCaseInsensitiveTESTPrefix` | `TestNormalizeSymbolRejectsTestPrefixForEveryTestnetProduct`, Spot/Perp client and WS rejection tests |
| `TestAsterSpotDiscoveryFiltersTESTSymbols`, `TestAsterPerpDiscoveryFiltersTESTSymbols`, `TestAsterDiscoveryFailsWhenOnlyTESTSymbolsRemain` | `TestFilterDiscoverySymbolsDropsTestCandidatesForEveryTestnetProduct`, `TestFilterDiscoverySymbolsFailsWhenNoSafeCandidateRemains` |
| `TestSubmitValidationRunsBeforeRiskReservation`, `TestValidSubmitUsesValidationConfiguredReservationAndOrdinarySubmitOnce` | `runtime/exec.TestSubmitValidationRejectsBeforeConfiguredRisk`, `TestSubmitWithRiskCallsCheckSubmissionDirectlyOnce`, `TestSubmitWithoutRiskSkipsRiskAndSubmitsOnce` |
| `TestConfiguredRiskDenialStopsBeforeDurableAndVenueEffects`, `TestConfiguredRiskReleaseForEveryOutcome`, concurrent reservation cases | `runtime/exec.TestConfiguredRiskDenialStopsBeforeDurableAndVenueEffects`, `TestConfiguredRiskReleaseForEveryOutcome`; `runtime/risk` submission reservation tests |
| `TestNadoSubmitNeverQueriesMaxOrderSize`, `TestNadoSubmitPreparesAndExecutesExactPayloadOnce`, `TestNadoSubmitRemembersDigestBeforeExecute` | Nado `TestNadoSubmitNeverQueriesMaxOrderSize`, `TestNadoSubmitPreparesAndExecutesExactPayloadOnce`, `TestNadoSubmitRemembersDigestBeforeExecute`; ordinary adapter `Submit` owns the transient signing object |
| `TestNadoSubmitReturnsDigestIdentity`, `TestNadoSubmitRequiresExactNonblankResponseDigest` | `TestNadoSubmitRequiresExactNonblankResponseDigest` verifies exact successful digest identity and rejects nil, blank, padded, or mismatched acknowledgements |
| `TestNadoSubmitTimeoutRetainsExactRecoveryCorrelation` | `TestNadoAmbiguousSubmitRetainsExactDigestCorrelation`, `TestNadoRejectsForeignResponseDigestAndRecoversSignedDigest` |
| `TestNadoSubmitPreparationFailureDoesNotExecute`, `TestNadoDirectConcurrentDuplicateClientIDDoesNotDoubleExecute` | Preparation-failure cases in `TestNadoPreparedMaterialRedactedOnEveryExit`; `TestNadoDirectConcurrentDuplicateClientIDDoesNotDoubleExecute` |
| `TestNadoAccountSummaryMapsHealths0AndHealths2WithoutFreeBalance` | `TestNadoSDKFixturesBackAdapterDiscoveryAndAccountMapping`, `TestNadoAccountFixturePreservesHealthAndSignedLiabilities` |
| `TestNadoPreparedMaterialRedactedOnEveryExit`, `TestNadoSubmitNeverLogsSignedMaterial` | Nado `TestNadoPreparedMaterialRedactedOnEveryExit`, `TestNadoSubmitNeverLogsSignedMaterial`; signed state remains adapter-local and is cleared on success and failure |
| `TestNadoSubmitRejectsResponseDigestMismatchAsPostHandoffAmbiguous`, exact response identity/cardinality cases | `TestNadoSubmitRejectsResponseDigestMismatchAsPostHandoffAmbiguous`, `TestNadoSubmitRequiresExactNonblankResponseDigest`, `TestNadoCancelRequiresOneExactAuthoritativeResponse`, `TestNadoCancelCommandOutcomeMatrixUsesVenueRejectedOnlyForCode2001` |
| Typed mass-status scope, partial retention, and warning-diagnostic cases | `TestNadoSpotMassStatusRetainsSuccessfulRowsWhenAnotherInstrumentFails`, `TestNadoMassStatusDistinguishesEmptyFromPreIOUnavailable`, `TestNadoExecutionMassStatusUsesMaximumFillLimitAndWarnsOnSaturation` |

Run the mapped offline surface with:

```sh
make test
make test-p6-offline
go test -race ./sdk/aster/common/... ./adapter/aster/... ./adapter/nado/... -count=1
```

## Nine-Venue Aggregate Evidence Contract

G008 records one hash-keyed, no-skip evidence row for each command below. The
rows are run serially on the frozen candidate; a later repository change
invalidates every row and restarts the ledger from Binance.

| Evidence row | Aggregate command |
| --- | --- |
| Binance Demo | `make test-binance-demo-acceptance` |
| OKX Demo | `make test-okx-demo-acceptance` |
| Bybit Demo | `make test-bybit-acceptance` |
| Bitget Demo | `make test-bitget-acceptance` |
| Gate Testnet | `make test-gate-testnet-acceptance` |
| Hyperliquid Testnet | `make test-hyperliquid-testnet-acceptance` |
| Lighter Testnet | `make test-lighter-testnet-acceptance` |
| Aster Testnet | `make test-aster-testnet-acceptance` |
| Nado Testnet | `NADO_TESTNET_MAX_NOTIONAL_USDT0=110 make test-nado-testnet-acceptance` |

## Historical Testnet Evidence

The rows below are preserved from 2026-07-13. They predate the generic runtime
submission and typed-coverage convergence, so they prove only that the
non-production accounts and venue lifecycles were usable at that time. They do
not certify the current tree. Only the nine frozen-hash G008 rows above can do
that.

| Row | Verdict | Evidence |
| --- | --- | --- |
| Aster Spot adapter | PASS | GTX place/cancel, IOC buy/sell, private execution/account events, no open orders, bounded base-asset delta |
| Aster Spot runtime | PASS | runtime-local risk rejection before venue handoff, bus/cache/account/portfolio readiness, reconcile, bounded base-asset delta |
| Aster Perp adapter | PASS | GTX place/cancel, IOC fill, reduce-only close, private events, flat venue position |
| Aster Perp runtime | PASS | runtime-local risk rejection before venue handoff, cache lifecycle, portfolio flat, final reconcile |
| Nado Spot adapter | PASS | funded-only/no-borrow, GTX place/cancel, IOC buy/sell, private fill evidence, no open orders |
| Nado Spot runtime | PASS | local rejection before venue I/O, cache cancel/fill lifecycle, reconcile, no open orders |
| Nado Perp adapter | PASS | discovered isolated-only mode, 1x opening margin, GTX place/cancel, IOC fill, reduce-only close, flat position |
| Nado Perp runtime | PASS | local rejection, cache lifecycle, reconcile, no open orders, flat position |

Nado's currently tradable Testnet products exceed the default `100 USDT0`
notional cap, so those historical rows used the explicit `110 USDT0` override. The
Testnet server acknowledged wildcard `order_update` subscriptions but emitted
no order events during these rows. Acceptance therefore requires a private fill
event and independently proves order state through command responses, REST open
orders, archive fills, runtime cache transitions, and final reconciliation.
