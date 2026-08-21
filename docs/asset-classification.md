# Instrument classification

BolterTrader treats market shape and economic asset class as two independent
facts. A perpetual deployed by a third-party builder is not automatically a
traditional asset, and a venue-native perpetual is not automatically crypto.

## Canonical fields

Every instrument crossing the Gateway boundary carries:

- `marketFamily`: `spot`, `venue_perpetual`, or `builder_perpetual`;
- `assetClass`: `unknown`, `crypto`, `equity`, `commodity`, `index`, `fx`,
  `rates`, `pre_ipo`, or `rwa_other`;
- optional source provenance and the venue's raw asset type/categories.

`unknown` is a valid result. The system never classifies an instrument from its
ticker, display name, quote currency, or a local hand-maintained symbol list.
Those shortcuts turn name collisions into false arbitrage opportunities.

The exchange adapter owns ingestion and normalization. `exchange.Instrument`
is the canonical record; Gateway projects it to the public catalog and joins
funding rows by exact native id, then exact full native symbol. Trading and the
web consume those projected facts and do not reclassify them.

## Official sources

### Lighter

The public `tokenlist` response supplies `market`, `asset_type`, and
`categories`. The adapter joins by exact symbol plus market (`SPOT`/`PERPS`).
`CRYPTO` maps to `crypto`; `RWA` is refined only by an explicit official
category and otherwise remains `rwa_other`. A missing or unrecognized
`asset_type` remains `unknown`.

### Hyperliquid

`allPerpMetas`, `perpDexs`, and spot token metadata form the complete native
and HIP-3 directory. HIP-3 identity keeps the full `{dex}:{coin}` namespace,
uses the official dex-index asset-id formula, and resolves the collateral token
from official spot metadata.

`perpCategories` is preferred classification provenance and
`perpConciseAnnotations` is the fallback. Only explicit general categories are
mapped. Free-form categories remain `unknown`; being a HIP-3 market alone is
not evidence that the underlying is TradFi.

Classification endpoints enrich a tradable catalog. If either endpoint is
temporarily unavailable, the last market directory remains usable and the
class is `unknown` instead of making public market data unavailable.

## Arbitrage families

The arbitrage opportunity API emits one asset family for every candidate:

- `crypto_continuous` for an officially classified crypto underlying, plus an
  exact canonical spot/perpetual pair whose class remains `unknown`;
- `tradfi_perpetual` for equity, commodity, index, FX, rates, pre-IPO, or other
  explicitly classified RWA underlyings;
- `unclassified` when no venue supplied enough evidence.

One known class may be combined with an unknown leg because unknown means
missing evidence. Two conflicting known classes are never paired, even when
their base and quote tickers match.

The web exposes these as separate candidate views. Three strategy engines now
share this classification layer without sharing their ranking formula:

1. cross-venue perpetual funding spread (`cross_perp_funding`);
2. Crypto Continuous spot-perpetual basis (`spot_perp_basis`);
3. TradFi same-underlying cross-venue perpetual spread
   (`cross_perp_spread`).

Funding is ranked from daily-normalized rates. TradFi perpetual spread is ranked
from the relative difference between official marks. Spot-perpetual basis is
discovered from the perpetual's bulk mark/index pair and joined to an exact spot
catalog instrument; this is an estimate, never an executable quote.

The exact spot/perpetual topology is sufficient to identify cash-and-carry even
when a venue publishes no economic class. This is not ticker classification:
the response remains `assetClass=unknown`, and any explicit non-crypto class or
conflicting official classifications still reject the pair.

Before either price strategy can be opened, Trading reads both exact order
books through Gateway and recomputes the edge from the long ask and short bid.
The sealed preview therefore contains a price the operator can cross rather
than a mark or index no venue offered. Positive spot-perpetual basis supports
cash-and-carry only: long spot and short perpetual. Negative/reverse basis is
reported as blocked because borrowing and locating spot inventory is not yet a
modeled authority.

Spot close orders are ordinary sells (`closing=true`, `reduceOnly=false`);
perpetual closes remain reduce-only. This distinction is carried in the durable
arbitrage intent so a mixed-product pair can be classified and closed without
sending an invalid reduce-only flag to the spot venue.
