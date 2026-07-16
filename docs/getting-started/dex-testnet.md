# Hyperliquid Perp Testnet Runtime Acceptance

> Canonical language: English. [Chinese mirror](../zh-CN/getting-started/dex-testnet.md)

This page owns the canonical DEX onboarding lifecycle. It runs the real
Hyperliquid standard-Perp Testnet adapter through a runtime node; it is a bounded
acceptance harness rather than a production trading guide.

## Resolve signer and owner identity

Set `HYPERLIQUID_TESTNET_PK` to a funded Testnet user or API-wallet private key.
The adapter derives the signer address and queries Hyperliquid `userRole` before
trading:

- a direct `user` role uses the signer address;
- an `agent` role resolves to the 0x owner returned by Hyperliquid;
- if the signer is an agent, set `HYPERLIQUID_ACCOUNT_ADDRESS` to that owner to
  make the expected identity explicit; a mismatch is rejected before writes;
- `vault` and `subAccount` user roles are rejected by this account model and are
  not supported by this walkthrough.

Optional controls are `HYPERLIQUID_TESTNET_PERP_SYMBOL` and
`HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC`, whose default is `100`. If no symbol is
set, the harness selects the first loaded standard-Perp instrument.

Use a dedicated Testnet account. The selected Perp must begin with no open order
and zero position, its book must have bids and asks, and the authoritative account
snapshot must contain enough free settlement collateral for the bounded opening
notional. Do not use a mainnet key or production endpoint.

## Run the single target

From the repository root, with no other external write target running:

```sh
make test-hyperliquid-testnet-runtime-perp
```

The recipe sets `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1` only for this
command, selects `TestHyperliquidPerpTestnetRuntimeAcceptance`, rejects a skipped
test, and gives the Go test process a six-minute timeout. Missing identity,
invalid owner resolution, dirty state, insufficient collateral, an empty book,
skip, timeout, or nonzero exit is not a pass.

## Understand the lifecycle

On a successful run, the harness performs this sequence:

1. It verifies that execution and account clients share one canonical account ID,
   checks selected-instrument open orders, loads a two-sided book, chooses a
   quantity within the notional envelope, and checks free settlement collateral.
2. It constructs the runtime node with account-required risk, calls `node.Resync`,
   applies exactly one authoritative `AccountState`, verifies freshness, balance
   and portfolio readiness, and confirms the selected account/runtime position is
   initially zero. It then starts streams and waits for active trading state.
3. It submits a post-only `GTX` limit buy, verifies that it remains unfilled,
   cancels it through runtime, and waits for both runtime-cache cancellation and
   authoritative venue terminal evidence.
4. It submits a marketable `IOC` limit buy, waits for the exact filled quantity,
   observes the long in both authoritative account positions and runtime
   portfolio, and requires matching private order/fill stream evidence.
5. It sends one `IOC` limit sell for the exact observed opening quantity with
   `ReduceOnly=true`. It waits for the authoritative selected-Perp position and
   runtime portfolio to become flat; an ambiguous close is not blindly repeated.
6. It reconciles again, reapplies a fresh account snapshot, and checks final venue,
   cache, account-position, and portfolio state.

The max-notional value is a local safety envelope, not a venue-capacity model.
Hyperliquid remains authoritative for margin, liquidity, and final acceptance.

## Interpret success and ambiguity

A zero exit proves only the loaded standard-Perp scope at that run's candidate
and time:

- no open order remains in the node's loaded standard-Perp scope;
- the authoritative account position for the selected Perp is exactly zero;
- runtime cache has no nonzero selected-Perp position and runtime portfolio net
  quantity for it is exactly zero;
- private order and fill evidence was observed for both the open and close.

It does not prove Spot, HIP-3, every account product, mainnet behavior, or future
availability.

After any nonzero or ambiguous exit, terminal state is unconfirmed even if
deferred cleanup ran. Preserve the exact validation client IDs and venue order
IDs for the resting, open, and close attempts. Before another write, inspect the
selected-scope authoritative open orders, the selected-Perp account position,
and runtime cache/portfolio state. Cleanup may cancel only tracked lifecycle
orders and may reduce only exposure bounded by confirmed lifecycle fills; when a
close outcome is ambiguous it must not send another close that could create a
short position. Never cancel or flatten unrelated account state.

## Related guidance

See the [Hyperliquid venue guide](../venues/hyperliquid.md),
[operations and recovery](../guides/operations-recovery.md), and
[testing reference](../reference/testing.md).
