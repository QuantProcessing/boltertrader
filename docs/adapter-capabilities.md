# Adapter Capabilities

This matrix is the live-only operating contract for the currently implemented
adapter subset. Unsupported report surfaces must return `contract.ErrNotSupported`;
open-only order reports must be treated as ambiguous absence during
reconciliation.

| Venue | Product | Market stream | Private order stream | Account stream | Submit | Cancel | Modify | Order status reports | Fill reports | Position reports | Mass status | Single-order query | Open-only caveat | Latency timestamps | Demo target |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| BINANCE | USD-M Perp | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | venue + runtime timestamps | make test-binance-demo-runtime-perp |
| BINANCE | Spot | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | venue + runtime timestamps | make test-binance-demo-spot |
| OKX | USDT-linear SWAP | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | venue + runtime timestamps | make test-okx-demo-runtime-perp |
| OKX | Spot cash | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | venue + runtime timestamps | make test-okx-demo-runtime-spot |

## Demo Scope

Default CI remains credential-free. Demo acceptance is explicit:

```sh
make test-binance-demo-acceptance
make test-okx-demo-acceptance
```

The tests skip when the required `*_DEMO_*` credentials are absent. Production
credentials are not accepted as fallback inputs for Demo acceptance.
