# Contributing an Adapter

[简体中文](../zh-CN/contributing/adapters.md)

> English is canonical. **Owner:** this page owns the runtime/adapter dependency
> boundary, normalized implementation obligations, capability-matrix updates,
> and adapter verification workflow. Venue pages own current product behavior.

## Preserve the dependency boundary

Production runtime code is venue-neutral. It may depend only on
`core/enums`, `core/model`, `core/contract`, and `core/clock`; it must not import
an adapter or SDK or branch on a venue name.

The implementation path is intentionally one-way:

1. `sdk/<venue>/` speaks the venue's authentication, request/response, and raw
   stream formats.
2. `adapter/<venue>/` resolves instruments and translates venue behavior into
   `core/contract` clients, models, errors, and typed event envelopes.
3. `runtime/` consumes only those normalized contracts.
4. Strategies use portable `Context` methods and never retain an adapter or SDK
   reference.

Runtime-aware test helpers may exercise an adapter, but that does not permit a
production adapter/runtime dependency cycle. See
[Architecture](../concepts/architecture.md).

## Implement the normalized contract honestly

An adapter product slice must provide a coherent instrument registry and only
the client surfaces it actually implements:

- `MarketDataClient` owns its `InstrumentProvider`, REST book/bars, concrete
  book/quote/trade subscriptions, market envelopes, and closure.
- Optional `DerivativeReferenceDataClient`, `OpenInterestClient`, and
  `FundingHistoryClient` implementations remain fine-grained. Current OI is a
  direct query, never a runtime cache or stream claim.
- `ExecutionClient` owns side-effect-free `ValidateSubmit`, synchronous
  acknowledged `Submit`, product-valid cancel/modify behavior, typed reports,
  execution envelopes, and closure.
- `AccountClient` owns mandatory authoritative `AccountState`, balances,
  positions, product-valid leverage/margin mutations, account envelopes, and
  closure. Account snapshots are not execution reports.
- When execution and account clients implement `AccountIDProvider`, both IDs
  must resolve the same logical scope before runtime startup.

Return `contract.ErrNotSupported` for an absent normalized method. Do not turn an
empty result, a configured stream bit, or a low-level SDK endpoint into a broader
claim.

`ValidateSubmit` must remain local and side-effect-free. Adapter/SDK code owns
venue conversion, signing, response cardinality, rejection mapping, and
ambiguous transport correlation; runtime owns the canonical portable submission
sequence described in [Execution and risk](../concepts/execution-risk.md). Do not
introduce a venue-specific prepared-order, lease, or capacity-admission protocol
into runtime.

That sequence is `ValidateSubmit` → optional configured venue-neutral
risk/reservation → durable intent → ordinary `Submit`.

## Conversion and event requirements

- Use `shopspring/decimal` for normalized price, quantity, balance, and notional
  values. Restrict `float64` to SDK/JSON boundaries and convert deliberately.
- Resolve venue symbols to stable `model.InstrumentID` values before publishing
  data or accepting normalized orders. Validate product, precision, tick/step,
  minimum, side, order type, time in force, and venue-specific combinations at
  the adapter boundary.
- Preserve client ID, venue order ID, trade ID, account ID, instrument ID,
  correlation, source, sequence, venue time, and flags in `EventMeta` whenever
  the source provides them. Do not advertise adapter receive/emit latency unless
  `TsAdapterRecv` and `TsAdapterEmit` are populated consistently.
- Make private-stream gaps and recoveries explicit with the normalized gap event
  where the SDK owns reconnect generations. Reconciliation must not silently
  manufacture terminal order causes or fills.
- Redact credentials and URL user information in errors, logs, and formatted
  configuration. Release transports and close event channels according to the
  contract without send-on-closed-channel races.

## Capability and report obligations

`Capabilities()` is a precise declaration, not a feature-family shortcut:

- distinguish static product inventory from configured dynamic stream
  availability;
- declare concrete product kinds and trading/account/market presence;
- keep book, quote, trade, derivative-reference, funding-history, and OI behavior
  separate even when a coarse Market bit is true;
- keep order, fill, position, and account-state report domains separate;
- list in mass status only the report domains directly owned by the execution
  adapter; and
- preserve the open-only caveat whenever open orders cannot establish a terminal
  reason or missing fills.

Before adding or changing a public runtime product row:

1. Update the concrete behavior and `Capabilities()` declaration. If the venue
   also exposes adapter-local `CapabilityRows()` (currently Bybit, Bitget, and
   Gate), update and test those rows in the same change.
2. Update the corresponding central `CapabilityRow` in
   `adapter/capabilities.go`. Keep its venue/product key unique and populate
   every stream, account-state, Submit/Cancel/Modify, report, mass-status,
   single-order, open-only, latency, and acceptance-target field from evidence.
3. Add an exact existing Make target; the matrix test rejects missing target
   names.
4. Update the canonical
   [capability matrix](../adapter-capabilities.md) and its Chinese mirror without
   changing the table schema or weakening a caveat.
5. Run `make test-capabilities` and the relevant adapter package tests.

SDK presence, dynamic configuration, and a venue's external API catalog do not
justify a static row. Use the [status ontology](../reference/glossary.md) and keep
absent/deferred facts in
[Unsupported, deferred, and SDK-only surfaces](../reference/unsupported.md).

## Required verification

For each new or changed product slice, cover all applicable layers:

1. SDK authentication/request serialization, response conversion, raw stream
   conversion, rejection behavior, and redaction.
2. Instrument discovery and venue/normalized symbol resolution.
3. Every implemented contract method plus explicit `ErrNotSupported` paths.
4. Capability declarations against the concrete methods and configured stream
   behavior.
5. Authoritative `AccountState`, identity agreement, readiness, and
   product-scoped reconciliation.
6. Execution/account event metadata, ordering, reconnect/gap recovery, and close
   behavior where streams exist.
7. Offline runtime acceptance with `runtime/runtimetest` or shared
   `adapter/internal/runtimeaccept` helpers.
8. An explicit, bounded Demo/Testnet adapter and runtime target when an official
   non-production path exists.

Aster/Nado conversion fixtures are governed by
`internal/fixtureaudit/testdata/aster_nado_manifest.json`. Each manifest entry
names the owning SDK, product, conversion kind, source, sanitization, and
negative-path status. Payloads under the owning package's `testdata` directory
must be sanitized synthetic derivatives of their declared source, never captured
account data. Most declared sources are official examples; an entry explicitly
declared `probe`, such as Aster OI, is derived from the recorded probe evidence.
Run `go test ./internal/fixtureaudit -count=1`; never add credentials, signed
preimages, or production order/account identifiers.

Default tests must remain offline and deterministic. Live-backed tests use
`internal/testenv`, exact Make targets, zero-skip enforcement, bounded notional,
and validation-owned cleanup. Run live targets serially. See
[Testing and evidence](../reference/testing.md),
[Configuration](../reference/configuration.md), and
[Operations and recovery](../guides/operations-recovery.md).

## Documentation handoff

A completed adapter change updates the venue page, capability matrix,
unsupported inventory when needed, configuration inventory for new variables,
and testing inventory for new canonical targets. Document implementation and
capabilities separately from dated external evidence; Demo/Testnet results never
imply production readiness.
