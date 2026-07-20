# Configuration

[简体中文](../zh-CN/reference/configuration.md)

> English is canonical. **Owner:** this page owns the exact shared
> Demo/Testnet environment-variable inventory, loading rules, write gates, and
> endpoint-profile safety. Venue pages own the operational meaning of each
> product and selector.

## Loading, precedence, and secrets

The acceptance helpers locate the repository root through `go.mod` and may load
its `.env`. Existing process variables always win. A missing `.env` is allowed.

Execution gates are never imported from `.env`. The loader rejects file-based
activation for names matching `RUN_*`, `*_REALTIME_WS`, `ENABLE_*`, names
containing `_ENABLE_`, `ALLOW_*`, or names containing `_ALLOW_`. Consequently,
`BOLTER_ENABLE_LIVE_READ_TESTS`, every venue write gate, and every unsafe custom
endpoint opt-in must be explicit in the process environment. Canonical Make
recipes set read/write gates command-locally.

Keep credential values, private keys, proxy credentials, and account residue out
of source, examples, logs, and version control. Configuration errors and string
representations must redact secrets and URL user information.

## Canonical write identity and gates

These are the complete canonical identity inputs for credentialed write
acceptance. Read-only targets may require a subset; the exact target remains the
authority.

| Environment | Caller-supplied write identity | Make recipe write gate |
| --- | --- | --- |
| Aster V3 Testnet | `ASTER_TESTNET_USER_ADDRESS`, `ASTER_TESTNET_SIGNER_PRIVATE_KEY` | `BOLTER_ENABLE_ASTER_TESTNET_WRITES` |
| Nado Testnet | `NADO_TESTNET_PRIVATE_KEY` | `BOLTER_ENABLE_NADO_TESTNET_WRITES` |
| Binance Demo | `BINANCE_DEMO_API_KEY`, `BINANCE_DEMO_API_SECRET` | `BOLTER_ENABLE_BINANCE_DEMO_WRITES` |
| OKX Demo | `OKX_DEMO_API_KEY`, `OKX_DEMO_API_SECRET`, `OKX_DEMO_API_PASSPHRASE` | `BOLTER_ENABLE_OKX_DEMO_WRITES` |
| Bybit Testnet | `BYBIT_TESTNET_API_KEY`, `BYBIT_TESTNET_API_SECRET` | `BOLTER_ENABLE_BYBIT_TESTNET_WRITES` |
| Bitget Demo/PAP | `BITGET_DEMO_API_KEY`, `BITGET_DEMO_SECRET_KEY`, `BITGET_DEMO_PASSPHRASE` | `BOLTER_ENABLE_BITGET_DEMO_WRITES` |
| Gate Testnet | `GATE_TESTNET_API_KEY`, `GATE_TESTNET_API_SECRET` | `BOLTER_ENABLE_GATE_TESTNET_WRITES` |
| Hyperliquid Testnet | `HYPERLIQUID_TESTNET_PK` | `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES` |
| Lighter Testnet | `LIGHTER_TESTNET_PRIVATE_KEY`, `LIGHTER_TESTNET_ACCOUNT_INDEX`, `LIGHTER_TESTNET_API_KEY_INDEX` | `BOLTER_ENABLE_LIGHTER_TESTNET_WRITES` |

Users normally run the exact target in [Testing and evidence](testing.md) rather
than exporting a write gate. Never substitute production credentials for these
non-production identities.

## Optional identity, symbol, and notional inputs

Notional limits are positive decimal safety bounds, not requested order sizes.
An “auto” symbol means the harness selects a suitable discovered instrument when
the variable is empty.

| Environment | Optional identity/selector inputs and defaults | Notional or quantity input and default |
| --- | --- | --- |
| Aster V3 Testnet | `ASTER_TESTNET_EXPECTED_SIGNER_ADDRESS` verifies signer identity; `ASTER_TESTNET_SPOT_SYMBOL` and `ASTER_TESTNET_PERP_SYMBOL` default to auto selection | `ASTER_TESTNET_MAX_NOTIONAL_USDT=100` |
| Nado Testnet | `NADO_TESTNET_SUBACCOUNT_NAME=default`; `NADO_TESTNET_SPOT_SYMBOL` and `NADO_TESTNET_PERP_SYMBOL` default to auto selection | `NADO_TESTNET_MAX_NOTIONAL_USDT0=150` |
| Binance Demo | `BINANCE_DEMO_SYMBOL=ETH-USDT` | `BINANCE_DEMO_MAX_NOTIONAL_USDT=100`; `BINANCE_DEMO_ORDER_QTY=0` means auto-sized |
| OKX Demo | `OKX_DEMO_SPOT_SYMBOL=ETH-USDT`; `OKX_DEMO_PERP_SYMBOL=ETH-USDT-SWAP` | `OKX_DEMO_MAX_NOTIONAL_USDT=100` |
| Bybit Testnet | `BYBIT_TESTNET_SYMBOL=BTCUSDT`; `BYBIT_TESTNET_USDT_PERP_SYMBOL=BTCUSDT`; `BYBIT_TESTNET_USDC_PERP_SYMBOL=BTCPERP` | `BYBIT_TESTNET_MAX_NOTIONAL_USDT=100`; `BYBIT_TESTNET_MAX_NOTIONAL_USDC=100` |
| Bitget Demo/PAP | `BITGET_DEMO_SYMBOL=BTCUSDT`; `BITGET_DEMO_USDT_PERP_SYMBOL=BTCUSDT`; `BITGET_DEMO_USDC_PERP_SYMBOL=BTCPERP` | `BITGET_DEMO_MAX_NOTIONAL_USDT=100`; `BITGET_DEMO_MAX_NOTIONAL_USDC=100` |
| Gate Testnet | `GATE_TESTNET_SPOT_SYMBOL=ETH_USDT`; `GATE_TESTNET_USDT_PERP_SYMBOL=BTC_USDT` | `GATE_TESTNET_MAX_NOTIONAL_USDT=100` |
| Hyperliquid Testnet | `HYPERLIQUID_ACCOUNT_ADDRESS` identifies the owner when an agent/API-wallet key signs; `HYPERLIQUID_TESTNET_SPOT_SYMBOL` and `HYPERLIQUID_TESTNET_PERP_SYMBOL` default to auto selection; `HYPERLIQUID_TESTNET_HIP3_SYMBOL` must be dex-qualified for HIP-3 | `HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC=100` |
| Lighter Testnet | `LIGHTER_TESTNET_SPOT_SYMBOL=ETH-USDC`; `LIGHTER_TESTNET_PERP_SYMBOL=ETH` | `LIGHTER_TESTNET_MAX_NOTIONAL_USDC=100` |

Identity fields are boundaries, not labels:

- Aster separates the user address, signing private key, and optional expected
  signer address.
- Nado's subaccount is part of unified-margin identity; it is not a
  currency-level balance partition.
- Hyperliquid's canonical harness supports the standard user role. Although
  `HYPERLIQUID_TESTNET_VAULT` is parsed, Vault and SubAccount user roles are
  rejected by the canonical path.
- Lighter requires both account index and API key index in addition to the
  private key.

## Endpoint profiles and override gates

Endpoint variables are advanced safety inputs. Never redirect a Demo/Testnet
write target to production.

### Fixed profiles

- Binance selects its built-in Demo REST/WS profile and has no acceptance
  endpoint override variable.
- Bybit external acceptance selects its built-in **Testnet** profile
  (`https://api-testnet.bybit.com`, `wss://stream-testnet.bybit.com/v5/public/*`,
  `wss://stream-testnet.bybit.com/v5/private`, and
  `wss://stream-testnet.bybit.com/v5/trade`). `BYBIT_DEMO_API_KEY` and
  `BYBIT_DEMO_API_SECRET` identify a different environment and are explicitly
  rejected as substitutes for Testnet credentials.
- Hyperliquid and Lighter select built-in Testnet profiles and expose no
  acceptance endpoint override variable.

### Exact-official-only profiles

Aster accepts the following variables only when their values exactly match the
built-in official Testnet endpoints:

- `ASTER_TESTNET_SPOT_REST_URL`
- `ASTER_TESTNET_SPOT_WS_URL`
- `ASTER_TESTNET_SPOT_USER_WS_URL`
- `ASTER_TESTNET_PERP_REST_URL`
- `ASTER_TESTNET_PERP_WS_URL`
- `ASTER_TESTNET_PERP_USER_WS_URL`

Nado applies the same exact-match rule to:

- `NADO_TESTNET_GATEWAY_URL`
- `NADO_TESTNET_GATEWAY_V2_URL`
- `NADO_TESTNET_ARCHIVE_URL`
- `NADO_TESTNET_ARCHIVE_V2_URL`
- `NADO_TESTNET_GATEWAY_WS_URL`
- `NADO_TESTNET_WS_URL`
- `NADO_TESTNET_TRIGGER_URL`

Gate reads accept `GATE_TESTNET_REST_BASE_URL`, `GATE_TESTNET_SPOT_WS_URL`, and
`GATE_TESTNET_USDT_FUTURES_WS_URL`. Credentialed writes require all three to
resolve to the known official Testnet profile. The older
`GATE_TESTNET_FUTURES_USDT_WS_URL` is a deprecated alias for the USDT Futures WS
variable.

### Guarded custom profiles

- `OKX_DEMO_HOST_PROFILE` accepts `global` (default), `eea`, or `custom`.
  Official `global`/`eea` credentialed writes forbid overrides. `custom`
  requires both `OKX_DEMO_REST_BASE_URL` and `OKX_DEMO_WS_BASE_URL`, plus
  command-local `BOLTER_ALLOW_OKX_DEMO_CUSTOM_WRITES=1`; credentialed writes
  require HTTPS/WSS and reject obvious website or production WebSocket hosts.
- Bitget's known Demo/PAP profile is `https://api.bitget.com` with
  `wss://wspap.bitget.com/v3/ws/public` and
  `wss://wspap.bitget.com/v3/ws/private`. Custom values for
  `BITGET_DEMO_REST_BASE_URL`, `BITGET_DEMO_PUBLIC_WS_URL`, and
  `BITGET_DEMO_PRIVATE_WS_URL` must be supplied together. Credentialed custom
  writes additionally require command-local
  `BOLTER_ALLOW_BITGET_DEMO_CUSTOM_WRITES=1`, HTTPS/WSS, and a non-production WS
  host.

## Deprecated Bitget compatibility aliases

Bitget's `BITGET_TESTNET_*` names map to the canonical Demo/PAP variables; they
do not select a second environment. New configuration must use the right-hand
names.

| Deprecated alias | Canonical variable |
| --- | --- |
| `BITGET_TESTNET_API_KEY` | `BITGET_DEMO_API_KEY` |
| `BITGET_TESTNET_SECRET_KEY` | `BITGET_DEMO_SECRET_KEY` |
| `BITGET_TESTNET_PASSPHRASE` | `BITGET_DEMO_PASSPHRASE` |
| `BITGET_TESTNET_SYMBOL` | `BITGET_DEMO_SYMBOL` |
| `BITGET_TESTNET_USDT_PERP_SYMBOL` | `BITGET_DEMO_USDT_PERP_SYMBOL` |
| `BITGET_TESTNET_USDC_PERP_SYMBOL` | `BITGET_DEMO_USDC_PERP_SYMBOL` |
| `BITGET_TESTNET_MAX_NOTIONAL_USDT` | `BITGET_DEMO_MAX_NOTIONAL_USDT` |
| `BITGET_TESTNET_MAX_NOTIONAL_USDC` | `BITGET_DEMO_MAX_NOTIONAL_USDC` |
| `BITGET_TESTNET_REST_BASE_URL` | `BITGET_DEMO_REST_BASE_URL` |
| `BITGET_TESTNET_PUBLIC_WS_URL` | `BITGET_DEMO_PUBLIC_WS_URL` |
| `BITGET_TESTNET_PRIVATE_WS_URL` | `BITGET_DEMO_PRIVATE_WS_URL` |

## Proxy input

`PROXY` accepts `http`, `https`, or `socks5` URLs and may contain credentials,
which must be redacted. The acceptance HTTP-client builders in
`internal/testenv` disable inherited `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY`
unless `PROXY` is set explicitly. WebSocket routing remains venue-specific; do
not infer it from the HTTP-client rule.

## Related guides

- [Prerequisites](../getting-started/prerequisites.md)
- [CEX Demo walkthrough](../getting-started/cex-demo.md)
- [DEX Testnet walkthrough](../getting-started/dex-testnet.md)
- [Unsupported and SDK-only surfaces](unsupported.md)
