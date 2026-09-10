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
Those shortcuts turn name collisions into comparisons between unrelated instruments.

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
