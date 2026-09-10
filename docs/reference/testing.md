# Testing

> Canonical language: English. [Chinese mirror](../zh-CN/reference/testing.md)

This page is the inventory of what you can run and what a green run proves.
The default Go suite uses local fixtures and no exchange credentials. Browser
mock mode reads live public markets, so its prices and network availability can
vary. No test target enables credentialed venue writes.

## The default path

```sh
make test
```

`go test -short ./...` across the repository. It needs no credentials, Docker,
external network or database; protocol tests may listen on local ports. Database
tests skip before reading their opt-in environment in short mode. This is the
gate to run before every commit.

| Target | Scope |
| --- | --- |
| `make test` | The whole repository, short mode |
| `make test-sdk` | `./sdk/...` only |
| `make test-exchange-offline` | `./exchange/...` with `-count=1` |
| `make test-exchange-race` | `./exchange/...` under `-race` |
| `make test-exchange-redaction` | The six tests that prove credentials never reach an error string |
| `make test-exchange-quality` | offline, then redaction, then race |
| `make test-gateway-offline` | `./gateway/...` and `./cmd/exchange-gateway` |
| `make test-gateway-client` | Shared wire DTOs, independent client/example, real Gateway HTTP/WS handler contracts |
| `make test-monitor` | Independent monitor replay, persistence, local Gateway socket faults and CLI |
| `make test-gateway-contract` | `contracts`, `domain`, `capability`, `store` |
| `make test-gateway-security` | Import boundary, OpenAPI, config redaction, crosswalk |
| `make test-gateway-race` | The gateway packages under `-race` |
| `make test-gateway` | offline, then race, then security |
| `make test-browser` | The web app: install, build, Vitest, Playwright |

`go test ./...` without `-short` is not the safe default. A handful of `sdk/`
tests read a venue's public endpoints when short mode is off. No Make target
runs them.

Two gateway targets need a real PostgreSQL and read `GATEWAY_TEST_POSTGRES_DSN`
from the environment; they fail fast when it is unset rather than skipping.
`make test-gateway-postgres` covers migrations, the command store, the audit
store, account ownership, encrypted registrations, and the real HTTP account flow
through Auth Edge, Gateway and Trading with fake venue clients. That flow verifies
login/CSRF, zero-balance registration, venue errors, current positions, database
reconnection and account deletion without trading. `make test-gateway-restart` covers the coordinator's
process-crash windows. `make test-compose-postgres` provisions a throwaway
cluster and runs both for you, which is the easier path.

## Container gates

```sh
make test-compose
```

Six sub-targets, in order:

| Target | What it proves |
| --- | --- |
| `test-compose-config` | The Compose file renders and the binaries load their config |
| `test-compose-build` | Both images build |
| `test-compose-security` | Non-root user, pinned entrypoint, no source or secrets in the image, all nine binaries present |
| `test-compose-smoke` | Migrators complete, services start, the gateway answers its own live and ready probes, and each service exits `0` within 15 seconds of `SIGTERM` |
| `test-compose-persistence` | Data survives a container recreate |
| `test-compose-postgres` | The real-PostgreSQL suites, against a throwaway cluster |

Each of these builds its own Compose project with generated credentials and
kernel-assigned ports. None of them requires `make up` to have run, and none of
them disturbs a stack that is already up.

## What happened to external acceptance

There used to be a third tier here: nine per-venue acceptance aggregates, twenty
gateway product-row targets, and the `BOLTER_ENABLE_<VENUE>_<ENV>_WRITES`
variables that armed them. All of it placed real orders on a venue's demo or
testnet environment.

It is gone, along with the environments it ran against. Every venue implemented
demo and testnet differently — a separate WebSocket backend here, a request
header there, a different signing domain or chain ID elsewhere — so the suite
cost more to keep working than the evidence it produced was worth. Nothing in
Go replaced it, because a replacement would have to spend real money.

One interactive path remains, and it is not a test target: the running stack.
An anonymous browser session is read-only and obtains live public market data
without venue credentials. Signing in and configuring an account enables the
private and write surfaces; those calls reach production. This repository
claims nothing about external availability.

## Gates and serial execution

**No recipe arms a credentialed write.** `TestMakefileRecipesNeverEnableCredentialedWrites`
reads every recipe in the Makefile and fails if one sets a write-enabling
variable or exports credential material. The default path is offline by
construction rather than by convention, which is what makes `make test` safe to
run without reading it first.

**Runs are serial.** The Makefile declares `.NOTPARALLEL`, which serializes
prerequisites within one invocation. It does not serialize separate `make`
processes.

## What a green run proves

A zero exit proves the normalized implementation still agrees with the fixtures
the suite carries, and nothing wider. It does not prove that a venue answers the
same way today, that an endpoint still exists, or that a credential would be
accepted. Every fixture is a photograph of a past response.

The PostgreSQL and Compose suites are the exception: they run against a real
database and real containers, so what they assert about those, they prove.

If something does go wrong against a real venue, the recovery rules did not
change with the harness. See
[Operations and recovery](../guides/operations-recovery.md).

## Publishing evidence

An Offline-conformant claim — the term is defined in the
[Glossary](glossary.md) — must name the exact product scope, the exact Make
target, the zero-skip result and its date, and any limitation that narrows it.

The zero-skip half is not something a green run tells you: `go test` exits zero
for a skipped test as readily as for a passed one, and exits zero when a `-run`
pattern matches nothing at all. `internal/testenv/cmd/noskipgotest` reads the
`-json` stream and fails on both — pass it the body of the target you are
claiming:

```bash
go run ./internal/testenv/cmd/noskipgotest -- -short ./exchange/... -count=1
```

`make test-exchange-offline` runs the ordinary short suite. Use the wrapper
above separately when claiming zero skips; an ordinary green test run is not
that evidence. A skip means the verified scope is narrower than the target name.

Publish that summary and nothing else. No credentials, no raw logs, no account
residuals, no local paths. Implementation, capability, and external evidence stay
three separate claims.

`make test-compose-postgres` also runs Trading storage and six real process-crash windows.
`make test-compose-recovery` creates a disposable stack and verifies restarts, a database outage and backup/restore.
For bounded public monitoring add `python3 scripts/acceptance/stack_recovery.py --public-seconds 180`.
