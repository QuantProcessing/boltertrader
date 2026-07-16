# Prerequisites

> Canonical language: English. [Chinese mirror](../zh-CN/getting-started/prerequisites.md)

This page owns the shared tool, access, and safety prerequisites for the offline,
Demo, and Testnet onboarding paths. Venue-specific credentials stay in their
walkthroughs and in the [configuration reference](../reference/configuration.md).

## Prepare the repository

Use Go 1.26, as declared by `go.mod`, together with Git and Make. Run commands
from the repository root. Confirm the toolchain and the venue-neutral baseline
before configuring any exchange account:

```sh
go version
make test-core
```

`make test-core` exercises core, runtime, and strategy packages without enabling
an external exchange read or write.

## Choose the smallest useful path

1. Start with the [offline runtime path](./offline-runtime.md). It requires no
   credentials and is the quickest way to learn runtime state and execution
   semantics.
2. Use [Binance Spot Demo](./cex-demo.md) for the canonical CEX lifecycle.
3. Use [Hyperliquid Perp Testnet](./dex-testnet.md) for the canonical DEX Perp
   lifecycle.

The external paths are bounded repository acceptance harnesses. They are not
example trading applications and do not establish production readiness.

## Prepare a non-production account

Use only credentials issued for the documented Demo or Testnet environment.
Prefer a dedicated account with deliberately limited funds and no unrelated
orders or positions in the product scope selected by the test. The walkthrough
will state the exact clean-state and funding preconditions.

The test environment can inherit variables from the shell or load the
repository's ignored `.env` file. Never commit that file, print credential
values, place secrets in command arguments, or substitute production credentials
or endpoints.

## Keep writes bounded and serial

Run the exact documented Make target. Its recipe enables the corresponding write
gate only for that process and applies the test's timeout and no-skip policy; you
normally should not set a write-gate variable yourself. Do not run external write
targets in parallel.

A nonzero, timed-out, skipped, or ambiguous run leaves final exchange state
unconfirmed. Follow the product-specific identity and state checks before another
write, and never cancel or flatten unrelated account state.

## Read next

See [testing and evidence](../reference/testing.md) for the verification tiers
and [operations and recovery](../guides/operations-recovery.md) for ambiguous
outcome handling.
