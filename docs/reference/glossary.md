# Glossary

[简体中文](../zh-CN/reference/glossary.md)

> English is canonical. **Owner:** this page owns BolterTrader's normative public
> terminology. Other pages use these meanings and link here instead of creating
> local definitions.

## Status and publication ontology

| Term | Normative meaning | Must not imply |
| --- | --- | --- |
| Implemented | Code for the named surface exists in the current tree | A capability declaration, external verification, or production readiness |
| Capability-advertised | A static or configured dynamic declaration advertises the exact product, report, command, or stream surface | That every method in a coarse category works, or that the surface has passed external acceptance |
| Demo/Testnet-certified | The named non-production target passed with zero skips for a named candidate, date, environment, product scope, and terminal assertion | Mainnet correctness, evergreen validity, account-wide cleanliness, or production readiness |
| Deferred | The product or surface is intentionally outside the current implementation slice for a stated reason | That the venue lacks it, or that it is permanently rejected |
| Unsupported | The exact normalized surface is absent or returns `contract.ErrNotSupported` | That the venue or its low-level API lacks an equivalent feature |
| SDK-only | Low-level venue SDK code exists without a runtime adapter/product row | Normalized adapter, runtime, strategy, or certification coverage |
| Production-ready | Separate operational evidence supports use in the stated production scope | A conclusion inferred from implementation, a capability bit, or Demo/Testnet evidence |
| Public-safe | Curated material is suitable for the public documentation tree: secrets and account residue are absent, operational evidence is summarized, and current source owns every claim | That raw logs, plans, traces, reviewer conversations, or private paths may be published |
| Development-generated | Temporary plans, raw validation output, traces, reviewer notes, and migration artifacts produced while developing or verifying a change | A public source of truth or content that may be copied into public pages without curation |

Bare **supported** and **certified** are not normative statuses. Name the exact
product and surface, then use one of the terms above.

## Execution and evidence vocabulary

| Term | Normative meaning |
| --- | --- |
| Acceptance harness | An explicit opt-in repository test that performs bounded real verification against a named non-production environment; it is not a reusable strategy application |
| Clean final state | A zero-exit terminal assertion within the exact symbol, product, account, and validation-owned-order scope of the harness |
| Ambiguous outcome | Venue handoff may have occurred but an authoritative result is not proven; automatic retry or compensating action is unsafe |
| Open-only caveat | Complete open-order coverage can prove a scoped order is no longer open, but cannot invent a terminal cause or missing fills |
| Runtime latency | Bus, application, callback, and command timing measured by runtime instrumentation; adapter receive/emit timing is a separate claim and may be absent |

## Data and report vocabulary

| Term | Normative meaning |
| --- | --- |
| Market stream | A coarse capability category; concrete book, quote, trade, or derivative-reference stream kinds must still be named |
| Derivative reference data | Funding, mark, index, or oracle values normalized into market events and cache state where the exact product implements them |
| Query-only OI | Current open interest fetched directly through the optional `OpenInterestClient`; it is neither subscribed nor stored in runtime cache |
| Account-state snapshot | The mandatory authoritative readiness snapshot returned by `AccountClient.AccountState`, including account scope, available balances, margin summary, identity, and freshness. Positions remain separate typed report/query evidence. |
| Mass status | The execution adapter's directly owned order, fill, and position report domains returned together; account-only snapshots do not become execution-owned report support through reconciliation |

## Architecture vocabulary

| Term | Normative meaning |
| --- | --- |
| Core | Venue-neutral enums, models, contracts, and clocks under `core/` |
| Runtime | Venue-neutral bus, cache, execution, portfolio, risk, reconciliation, and strategy orchestration under `runtime/` |
| Adapter | The translation and behavioral boundary between normalized contracts and a venue SDK/API under `adapter/<venue>/` |
| SDK | Low-level official-API-shaped client code under `sdk/<venue>/`; its presence alone does not establish a runtime product |

See [Architecture](../concepts/architecture.md), the
[capability matrix](../adapter-capabilities.md),
[Testing and evidence](testing.md), and
[Unsupported, deferred, and SDK-only surfaces](unsupported.md) for concrete use
of this vocabulary.
