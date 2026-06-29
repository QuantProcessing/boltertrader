# OKX SDK Guide And Core Concepts

This document introduces the OKX V5 API concepts needed when using
`github.com/QuantProcessing/exchanges/sdk/okx` directly: `instId`, `tdMode`,
`posSide`, and the difference between `instType`, `instFamily`, and concrete
instrument identifiers.

For the Chinese version, see `README_CN.md`.

## Instrument ID (`instId`)

OKX identifies every tradable instrument with `instId`. You pass it to calls
such as `GetTicker`, `GetOrderBook`, and `PlaceOrder`.

| Product | Format | Example | Notes |
| --- | --- | --- | --- |
| Spot | `BASE-QUOTE` | `BTC-USDT` | BTC spot quoted in USDT. |
| Swap | `BASE-QUOTE-SWAP` | `BTC-USDT-SWAP` | USDT-margined perpetual. |
| Swap | `BASE-USD-SWAP` | `BTC-USD-SWAP` | Coin-margined inverse perpetual. |
| Futures | `BASE-QUOTE-YYMMDD` | `BTC-USDT-250328` | USDT-margined delivery contract. |
| Option | `BASE-USD-YYMMDD-STRIKE-C/P` | `BTC-USD-250328-100000-C` | BTC option, call or put. |

When a user gives only a base symbol such as `BTC`, construct the final OKX
instrument from the trading context:

- spot BTC/USDT: `BTC` -> `BTC-USDT`;
- USDT-margined BTC perpetual: `BTC` -> `BTC-USDT-SWAP`;
- coin-margined BTC perpetual: `BTC` -> `BTC-USD-SWAP`.

Use `client.GetInstruments(ctx, "SWAP")` or the equivalent product type to
build an instrument cache instead of guessing listed markets.

## Trade Mode (`tdMode`)

`tdMode` controls how margin is used when placing an order.

| Value | Meaning | Typical use |
| --- | --- | --- |
| `cash` | Non-margin cash mode | Spot orders. |
| `cross` | Cross margin | Margin and derivatives where account collateral is shared. |
| `isolated` | Isolated margin | Position-scoped risk control. |

## Position Side (`posSide`)

For derivatives, especially in hedge mode, `posSide` tells OKX which side of
the position the order targets.

| Value | Meaning |
| --- | --- |
| `long` | Open or close the long side. |
| `short` | Open or close the short side. |
| `net` | Net-position mode; also the practical default for spot/cash flows. |

## Quick Examples

Fetch a ticker:

```go
instID := "BTC-USDT" // spot
// instID := "BTC-USDT-SWAP" // perpetual swap

ticker, err := client.GetTicker(ctx, instID)
fmt.Println("last:", ticker[0].Last)
```

Place a BTC spot limit order:

```go
req := &okx.OrderRequest{
    InstId:  "BTC-USDT",
    TdMode:  "cash",
    Side:    "buy",
    OrdType: "limit",
    Px:      "95000",
    Sz:      "0.1",
}
resp, err := client.PlaceOrder(ctx, req)
```

Place an isolated BTC perpetual long:

```go
req := &okx.OrderRequest{
    InstId:  "BTC-USDT-SWAP",
    TdMode:  "isolated",
    Side:    "buy",
    PosSide: "long",
    OrdType: "market",
    Sz:      "1",
}
resp, err := client.PlaceOrder(ctx, req)
```

## Size Units

`Sz` means different things for different OKX products:

- spot: base asset quantity, for example `1` means 1 BTC;
- contracts: number of contracts. Query instrument metadata such as `ctVal`
  and `ctMult` before converting a target coin notional into contract size.

## `instType` Versus `instFamily`

`instType` selects the market family such as `SPOT`, `SWAP`, `FUTURES`,
`OPTION`, or `MARGIN`.

`instFamily` groups related derivative instruments by underlying, such as
`BTC-USD` or `BTC-USDT`.

`instId` is the concrete final market, such as `BTC-USDT-SWAP`.
