# Venue Guides

[Chinese mirror](../zh-CN/venues/README.md) · This English page is canonical.

## Ownership and scope

This index routes to venue-specific environment, product, configuration, order,
account, and acceptance notes. Venue pages are the owner of venue-specific
semantics; the detailed per-product truth table remains in the 21-row
[capability matrix](../adapter-capabilities.md).

| Venue | Public non-production environment | Runtime product pages |
| --- | --- | --- |
| [Binance](binance.md) | Demo | Spot, USD-M Perp |
| [OKX](okx.md) | Demo | Spot cash, USDT-linear SWAP |
| [Bybit](bybit.md) | Demo Trading | Spot, USDT-linear Perp, USDC-linear Perp |
| [Bitget](bitget.md) | Demo/PAP | Spot, USDT-linear Perp, USDC-linear Perp |
| [Gate](gate.md) | Testnet | Spot, USDT-linear Perp; USDC deferred |
| [Hyperliquid](hyperliquid.md) | Testnet | Spot, Perp, HIP-3 Perp |
| [Lighter](lighter.md) | Testnet | Spot, Perp |
| [Aster](aster.md) | Testnet | Spot, USDT-linear Perp |
| [Nado](nado.md) | Testnet | Spot no-borrow, Perp |

## How to read a venue page

- Request and subscription methods are listed separately. A coarse `Market`,
  `Execution`, or `Account` stream flag does not prove that every normalized
  subscription method is wired.
- `Submit`, `Cancel`, `CancelAll`, and `Modify` are reported independently of
  stream support. A venue may reconcile commands through REST without private
  stream dispatch.
- Report coverage distinguishes exact single-order evidence, open-order
  snapshots, bounded fill history, positions, and account state. An open-only
  absence is not terminal-order proof unless the venue page says otherwise.
- Reference data is Perp-only unless stated otherwise. Current open interest is
  queried directly from the venue and is not a retained runtime cache.
- A recorded successful zero-skip Demo/Testnet run proves only its named product,
  lifecycle, and terminal assertions. Target existence alone proves nothing
  about current external behavior, and no run establishes production readiness
  or unnamed products, account modes, or stream dimensions.

The documented acceptance targets use the repository's zero-skip runner. Read
[Testing](../reference/testing.md) for gate and cleanup semantics and
[Configuration](../reference/configuration.md) for the canonical credential and
safety-gate names. SDK-only venues and deferred dimensions are owned by
[Unsupported and Deferred Features](../reference/unsupported.md).
