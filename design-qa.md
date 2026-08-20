# Trade terminal design QA

## Evidence

- Source visual truth: the Lighter screenshot supplied in this thread, plus the
  live public BTC trade view at <https://app.lighter.xyz/trade/BTC?locale=zh>.
- Implementation: <http://127.0.0.1:5174/trade>.
- Implementation screenshot: `/private/tmp/bolter-trade-lighter-pass.png`.
- Viewport: 1920×929 CSS px in Chrome. Both browser captures were normalized
  to 1920×929 output pixels by the browser capture layer; source and
  implementation used the same browser viewport and density treatment.
- State: dark desktop terminal, BTC perpetual selected, 15m chart on Bolter;
  final focused inspection used OKX BTC-USDT so public depth, funding, open
  interest and the complete ticket state were visible. Lighter's live capture
  retained its own persisted chart interval, so chart *content* is not used for
  candle-for-candle comparison; frame, density and component anatomy are.

## Comparison history

### Pass 0 — supplied screenshots

The original Bolter capture had six actionable differences:

- [P1] Header navigation was sparse, under-emphasized and used an underline
  active state instead of Lighter's compact filled selection.
- [P1] The market strip opened on a thin catalog-first instrument and omitted
  a useful funding percentage, open interest hierarchy and next-funding slot.
- [P1] `fitContent()` left a large unused history gutter in the chart on first
  load.
- [P1] The 256px order book lacked a title/anatomy hierarchy and had too few
  levels for a desktop terminal.
- [P1] The 256px bottom deck dominated the chart while leaving the working
  table visually unfinished; disabled queries showed permanent skeletons.
- [P1] The ticket was a rounded card inside a padded rail instead of a flat
  terminal panel and lacked a compact account/order summary.

Fixes made: measured layout tokens were written to `design.md`; the navigation,
market strip, chart toolbar/legend/initial range, 340px book, 190px blotter and
340px flat ticket were rebuilt. The default market now prefers Binance BTC
perpetual and falls back to another ready BTC market before catalog order.

### Pass 1 — full-view browser comparison

The Lighter reference and the first revised Bolter render were captured in the
same comparison input at 1920×929. Header hierarchy, column proportions,
surface palette, chart utilization, numeric typography and lower-deck rhythm
were aligned. One focused surface could not yet be judged: Binance public
depth was unavailable in the mock session even though candles were live.

Resolution: switched the implementation through its real instrument picker to
OKX BTC-USDT. The public fact then exposed mark price, funding, open interest,
next funding and 24 visible clickable depth rows. This was a QA-state change,
not fabricated fixture data.

### Pass 2 — focused book, ticket and blotter inspection

Post-fix evidence is `/private/tmp/bolter-trade-lighter-pass.png`.

- Order book: three tabular columns stay aligned; ask/bid hue is paired with
  position and depth fill; the spread row is distinct; 24 rows were visible at
  the captured height; clicking a row filled the ticket with `64352.9`.
- Ticket: Buy/Sell and Limit/Market label controls changed their native radio
  state and were restored to Buy/Limit. Unsupported balance, liquidation,
  margin and fee values remain honest em dashes.
- Blotter: Open orders and Positions tabs both reported selected state when
  clicked, and the final state was restored to Positions.
- Viewport resilience: at 1440×900, document `scrollWidth` equalled
  `innerWidth` (1440) and `scrollHeight` equalled `innerHeight` (900); no page
  overflow hid the book or ticket.
- Console: no application errors from `127.0.0.1:5174`.

### Pass 3 — follow-up density and readability inspection

The follow-up was checked in Chrome at a 1920px viewport against live mock-mode
public market data.

- Order book: the side selector is a compact 174px control on the left. Ask and
  bid rows now form one ladder, so a venue returning fewer grouped levels no
  longer leaves an empty band above the best ask; spare height falls after the
  last bid instead of splitting the market.
- Instrument identity: positions, orders, fills, portfolio records and
  arbitrage legs display `BTC-USDT` with `Binance · Perp` context. The canonical
  id remains available as a title and remains unchanged on every API path.
- Spot market strip: ETH-USDT renders inside a 36px two-line selector. Mark,
  open interest, funding and next-funding slots are absent when unpublished;
  the remaining Last, Today, Volume, Open, High and Low facts close the gap.
- Reading width: `/funding` and `/funding/arbitrage` measured exactly 1600px,
  centered at x=160 in a 1920px viewport. `/trade` remains full-bleed.
- Arbitrage blockers: `instrument_unlisted` renders as “所选账户无对应合约”,
  and the selected ticket explains that the market exists on the board while
  the account catalog has no matching contract.
- Console: no application-origin error or warning; the only captured messages
  came from installed wallet extensions.

## Required fidelity surfaces

- Fonts and typography: pass. Inter remains the label face, Geist Mono remains
  the streaming-number face, visible sizes stay in the 11–15px terminal range,
  and active labels use weight plus fill rather than color alone.
- Spacing and layout rhythm: pass. 52px nav, 44px market strip, 340px book,
  340px ticket and 190px deck match the documented reference anatomy. The
  chart has no large initial left gutter.
- Colors and tokens: pass. Near-black panel steps, low-alpha white seams,
  green/red semantic pairs and shallow 1–4px radii match the source language.
- Image quality and asset fidelity: pass / not applicable. This screen has no
  photographic or illustrative assets. All visible interface icons use the
  project's Lucide icon library; no emoji, CSS drawings or inline SVG
  substitutes were introduced.
- Copy and content: pass. Labels are concise and terminal-specific. Values the
  backend does not publish are absent or shown as `—`; no Lighter-only account
  value is fabricated.
- Accessibility and interaction: pass. Controls retain native radio/select/
  input semantics, focus treatment remains global, state is not hue-only, and
  the 1440px desktop check has no hidden persistent controls.

## Findings

No actionable P0, P1 or P2 differences remain for the six requested regions.

Accepted product differences: Bolter does not copy Lighter's announcement bar,
wallet/RFQ flow, leverage control, drawing toolbar, stop-loss builder or layout
editor because those capabilities are outside the current product surface.
Their absence does not change the requested hierarchy or make up exchange or
account data.

## Follow-up polish

- [P3] If chart drawing becomes a product requirement, add a real chart-tool
  integration rather than decorative toolbar icons.
- [P3] A future backend account-summary projection can replace the ticket's em
  dashes with real available margin, position margin, liquidation and fee
  values without changing the layout.

## Implementation checklist

- [x] Document the measured Lighter-derived design system in `design.md`.
- [x] Align header, market facts and terminal grid.
- [x] Remove the chart's initial empty history gutter.
- [x] Rebuild order-book hierarchy and depth density.
- [x] Rebuild the ticket as a flat terminal panel.
- [x] Reorder and tighten the positions/orders/fills workspace.
- [x] Type-check, unit-test and browser-QA the result.

final result: passed
