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

## Arbitrage high-fidelity workspace — final browser pass

### Visual sources and implementation evidence

- Approved sources:
  - `docs/design/arbitrage/crypto-basis.png`
  - `docs/design/arbitrage/crypto-funding.png`
  - `docs/design/arbitrage/tradfi-funding.png`
  - `docs/design/arbitrage/tradfi-spread.png`
- Final browser captures:
  - `docs/design/arbitrage/implementation-final-crypto-basis.jpg`
  - `docs/design/arbitrage/implementation-final-crypto-funding.jpg`
  - `docs/design/arbitrage/implementation-final-tradfi-funding.jpg`
  - `docs/design/arbitrage/implementation-final-tradfi-spread.jpg`
- Same-input source/implementation comparisons:
  - `docs/design/arbitrage/qa-final-crypto-basis.png`
  - `docs/design/arbitrage/qa-final-crypto-funding.png`
  - `docs/design/arbitrage/qa-final-tradfi-funding.png`
  - `docs/design/arbitrage/qa-final-tradfi-spread.png`
- Focused evidence:
  - `docs/design/arbitrage/implementation-final-ticket.jpg`
  - `docs/design/arbitrage/implementation-final-open-pairs.jpg`
- Viewport: 1487×1058 CSS px at device scale 1, matching the approved source
  canvas. The Chrome capture provider returned a 1487×1027 raster, so the
  comparison input preserves its scale and pads the final 31px instead of
  stretching the implementation.
- State: authenticated operator with four real local accounts and live public
  market data. No account, preview or order was created during visual QA.

### Implemented visual contract

- A real transparent Bolter wordmark replaces the generic icon/name pairing in
  the global header and retains the compact 52px terminal navigation rhythm.
- All four strategies share the approved information architecture: dense
  ranked opportunities on the left, dominant historical evidence in the
  center, execution on the right and a persistent 190px open-pairs shelf.
- Crypto basis renders spot/perpetual prices, basis history and funding carry;
  crypto cross-venue funding renders daily spread, APR and U-notional OI.
- TradFi funding and cross-venue spread retain venue-native classification
  provenance. Price-spread history adds the observed mean, standard deviation,
  Z-score and ±1σ guides when synchronized candle history exists.
- The execution rail uses real account compatibility, reference prices,
  available balances, order type, quantity, preview sealing and two-leg open
  commands. Unsupported leverage, buying-power percentages and session controls
  from the visual concept were not added as decorative or fabricated controls.
- Missing, stale and unavailable values remain `—` or an explicit empty state;
  no mock price, balance, position or open pair is synthesized.

### Comparison history and fixes

- Pass 1 found four actionable differences: 56px opportunity rows reduced
  information density; chart canvases could visually cover the lower shelf;
  five-second polling could reselect the newly ranked first row; and long
  TradFi symbols wrapped against classification/status metadata.
- Pass 2 reduced rows to 44px, clipped both chart viewports to their owned
  region, persisted selection by opportunity id and made the snapshot title
  single-line with bounded metadata truncation.
- Pass 3 added real spread statistics and responsive desktop rail widths. At
  1200×800, the rails resolve to 360px/340px and leave a 500px evidence panel;
  document and body width/height exactly match the viewport. At 1280×900 and
  1487×1058, no page-level overflow hides the ticket or open-pairs shelf.
- Final side-by-side review confirms the source hierarchy, typography scale,
  near-black surface steps, restrained borders, numeric density and green/red
  trading semantics across all four routes. Live symbols and values naturally
  differ from the conceptual reference content.

### Interaction and runtime verification

- Search reduced the live basis list to three BTC matches; the executable-only
  control reduced it to one and restored cleanly.
- Selecting the second funding opportunity changed the center from
  `ANSEM-USDC` to `DATA-USDC`; swapping legs updated the route identities in
  the URL and preserved the selected opportunity.
- The ticket's native account, Market/Limit and quantity controls remain
  keyboard-accessible. Preview was intentionally not submitted during QA.
- Application-origin console output contained no errors or warnings. Captured
  EventEmitter and provider warnings came only from installed wallet browser
  extensions; the application emitted Vite/React development information.
- `npm --prefix web run build`: passed.
- `npm --prefix web test`: 17 files and 121 tests passed.
- `git diff --check`: passed.

### Remaining non-blocking differences

- [P3] The approved concepts show sample leverage, percentage sizing, market
  session and open-position analytics that are not present in the current
  arbitrage API. They should be added only with real backend contracts.
- [P3] Some live TradFi pairs do not have two aligned candle series in the
  selected window. Those views correctly show the specific history-empty state
  instead of rendering a cosmetic chart.

## Wide-screen regression audit — 2026-08-21

- Source: `docs/design/arbitrage/crypto-basis.png` (1487×1058).
- Current implementation: `docs/design/arbitrage/audit-current-wide.jpg`
  (1875×829 CSS px capture, Chrome at DPR 2).
- Combined evidence: `docs/design/arbitrage/qa-blocked-wide.png`; the source is
  scaled to the implementation height without changing its aspect ratio.
- [P1] The strategy workspace is constrained to the generic 1600px reading
  width, producing side gutters absent from the approved terminal composition.
- [P1] The opportunity rail omits the source filters, route/status columns and
  34px single-line row density.
- [P1] Center and ticket regions are flush seams rather than the source's
  separately bounded panels with an 8px working gutter.
- [P1] The price-history chart can draw the funding series while an opaque
  “history unavailable” state covers the same plot, creating contradictory
  evidence.
- [P1] The execution rail lacks the quantity percentage controls and the
  estimate hierarchy visible in the approved target.
- [P2] The history surface lacks the compact evidence toolbar and navigator,
  leaving the wide plot visually sparse.

The prior pass accepted these as product differences without comparing the
actual 1875px authenticated state. That acceptance is withdrawn. Re-capture all
four strategies after the fixes above before changing this result.

## Wide-screen resolution — 2026-08-21

- Rebuilt all four strategy workspaces against the approved 1487×1058 sources
  and captured the production build at the same viewport:
  - `docs/design/arbitrage/qa-final-crypto-basis.png`
  - `docs/design/arbitrage/qa-final-crypto-funding.png`
  - `docs/design/arbitrage/qa-final-tradfi-funding.png`
  - `docs/design/arbitrage/qa-final-tradfi-spread.png`
- Removed the generic reading-width constraint from the four strategy routes.
  The workspace now uses a full-bleed three-column terminal grid with explicit
  8px gutters, a 438px opportunity rail, a flexible evidence panel, a 398px
  execution rail and a persistent 190px open-pairs shelf.
- Restored the source hierarchy and density: 44px context header, compact
  metrics, search/status/direction/filter controls, 34px opportunity rows,
  aligned numeric columns and locally formatted `BASE/QUOTE` identities.
- The evidence panel now uses the source's green edge and neutral funding
  semantics, includes a real navigator and never renders an opaque empty state
  over a non-empty series. Funding-only history is labelled explicitly.
- The execution rail now exposes actual available balance, maximum quantity,
  25/50/75/100 sizing, order type and calculated leg/edge/carry estimates.
  Unsupported leverage and margin-mode controls remain absent rather than
  pretending the current API can execute them.
- Search, opportunity selection and 25% sizing were exercised against the live
  production build. No preview or order was submitted. Production captures
  showed no visible runtime error boundary; the selected Chrome session did not
  expose a console-stream capability, so build and automated tests provide the
  runtime error gate for this pass.
- `npm --prefix web run build`: passed (only the existing chunk-size advisory).
- `npm --prefix web test -- --run`: 17 files and 121 tests passed.
- `git diff --check`: passed before this record was finalized.

### Remaining P3 product/data differences

- The backend currently supports 7d/30d history, not the concept's 90d range.
- Leverage, margin mode, market session and richer open-pair analytics require
  real contracts before they can be added.
- A live strategy with no open pairs or no aligned two-leg history shows its
  truthful empty state; mock content is not inserted to imitate the concept.

No P0, P1 or P2 visual-fidelity issue remains in the four approved strategy
routes.

## Information-hierarchy refinement — 2026-08-21

- Baseline: `docs/design/arbitrage/audit-pass5-current.png`.
- Final crypto-basis state: `docs/design/arbitrage/audit-pass5-final-basis-24h.png`.
- Final crypto-funding state: `docs/design/arbitrage/audit-pass5-final-funding-24h.png`.
- Same-route comparison: `docs/design/arbitrage/qa-pass5-basis-before-after.png`.
- Viewport and raster: 1875×829 CSS px at device scale 1. The same authenticated
  Chrome session and production container were used; no preview or order was
  submitted.

### Findings and resolution

- [P1 resolved] The 44px breadcrumb/summary strip repeated low-value context and
  competed with the evidence panel. It was removed; refresh moved into the
  opportunity toolbar.
- [P1 resolved] The narrow opportunity rail collapsed fee/basis percentages into
  one unreadable run. Desktop rail width is now 468px with explicit numeric
  columns and whitespace; the 1200px layout uses a compact column map without
  page overflow.
- [P2 resolved] History offered only 7d/30d. A 24h control now requests the
  existing 7d backend contract and clips to the trailing day relative to the
  newest observation, avoiding a second server endpoint.
- [P2 resolved] The footer exposed backend implementation names. It now reports
  venue connectivity from the existing funding-board projection, conservatively
  aggregating route profiles into live, delayed and unavailable venue counts.
- [P2 resolved] The chart felt oversized because the summary strip, narrow list
  and tall empty shelf created competing planes. Removing the strip and widening
  the decision rail restored a clear left-to-right hierarchy; the chart remains
  the dominant evidence surface without increasing its owned height.

### Verification

- Visible 24h control was exercised on crypto basis and cross-venue funding;
  native radio state reported selected after each click.
- Final footer reported live and delayed venue groups with the underlying venue
  names exposed as accessible detail.
- `npm --prefix web test -- --run`: 19 files and 124 tests passed.
- `npm --prefix web run build`: passed (existing chunk-size advisory only).
- `git diff --check`: passed.

No P0, P1 or P2 issue remains from this refinement.

## Opportunity sorting and chart hierarchy — 2026-08-21

- Baseline: `docs/design/arbitrage/audit-sort-chart-baseline.png`.
- Final default ranking: `docs/design/arbitrage/audit-sort-chart-final-default.png`.
- Final OI ranking: `docs/design/arbitrage/audit-sort-chart-final-oi.png`.
- Full comparison: `docs/design/arbitrage/qa-sort-chart-before-after.png`.
- Focused comparisons: `docs/design/arbitrage/qa-sort-list-focused.png` and
  `docs/design/arbitrage/qa-sort-chart-focused.png`.
- Viewport and raster: 1875×829 CSS px at device scale 1. The same authenticated
  Chrome session and production container were used; no preview or order was
  submitted.

### Findings and resolution

- [P1 resolved] Generic “全部” and direction filters consumed the highest-value
  toolbar area without expressing how opportunities were ranked. They were
  removed; search and refresh remain.
- [P1 resolved] Funding spread, APR and OI were display-only. Their column
  headers now use the shared sortable-table affordance, expose the active
  direction, toggle ascending/descending, and keep missing values last in both
  directions.
- [P2 resolved] The other strategy lists used different column models and did
  not consistently expose OI. All four strategy routes now share the same
  six-column rhythm and sort their primary edge, carry/funding and OI fields.
- [P2 resolved] The 3px pale-purple derived series dominated native venue facts
  and used an accent color without semantic meaning. Derived edge/spread history
  is now a 2px muted neutral line; native long/short legs remain 1px semantic
  green/red lines, and the navigator follows the same neutral treatment.
- [P2 resolved] The opportunity list and the existing funding table previously
  had separate sort icon implementations. Both now use one component with the
  same icon sizing, spacing, active state and accessible `aria-sort` contract.

### Interaction and verification

- Cross-venue funding OI descending placed BTC/USDC (2.5B U) first.
- Cross-venue funding APR descending placed DATA/USDC first.
- Spot-perpetual basis OI descending placed BTC/USDC (2.5B U) first.
- Missing numeric values remained below known values instead of being treated as
  zero. Search, selection and route state remained intact while sorting.
- `npm --prefix web test -- --run`: 20 files and 126 tests passed.
- `npm --prefix web run build`: passed (existing chunk-size advisory only).
- `git diff --check`: passed before this record was finalized.

No P0, P1 or P2 issue remains from this refinement.

## Strategy identity, review fixes and bounded workspace — 2026-08-21

- Before state: `docs/design/arbitrage/audit-context-layout-baseline.png`.
- Final live-data state: `docs/design/arbitrage/audit-context-layout-final.png`.
- Before/after comparison: `docs/design/arbitrage/qa-context-layout-before-after.png`.
- Context-strip reference/final comparison:
  `docs/design/arbitrage/qa-context-layout-reference-final.png`.
- The source captures are 1875×829 and the final production capture is
  1920×873. The comparisons normalize the final raster to 1875×829 so layout,
  density and visual hierarchy can be inspected side by side. Both captures use
  the same desktop breakpoint and authenticated product state.

### Independent review findings and resolution

- [P1 resolved] A perp-perp opportunity with OI on only one leg previously
  displayed and ranked that one value as hedge capacity. Perp-perp strategies
  now require OI from both legs and use the smaller value; spot-perp basis uses
  only its perpetual leg. Missing capacity renders as `—` and remains last in
  both sort directions.
- [P1 resolved] Negative basis, spread or funding-edge snapshot values could be
  styled as positive. Snapshot metrics now use directional coloring only for
  directional quantities; reference prices stay neutral.
- [P2 resolved] The shared `DataTable` sort control had dropped the pointer
  event required by TanStack's Shift multi-sort contract. The event is forwarded
  again and a regression test covers two simultaneously sorted columns.
- [P2 resolved] The strategy opportunity list mixed isolated ARIA table roles
  with a real `ul`/button list. It now keeps valid list semantics and announces
  each sort column and current direction through the button's accessible name.
- [P2 resolved] Sort targets now have a 24px desktop minimum and a 44px
  coarse-pointer minimum. Inactive icons use the readable dim token.
- [P2 resolved] Funding history axes include `%/d`; the derived line and legend
  are neutral at 2px while native long/short legs remain 1px semantic lines.
  The navigator also supports a red down tone when only the short leg exists.

### Navigation and composition

- The intermediate `套利策略` menu item and overview page were removed. The
  arbitrage menu now exposes the four operator tasks directly, grouped by
  Crypto Continuous and TradFi Perpetual. `/arbitrage` redirects to the default
  spot-perpetual basis workspace so old entry points remain useful.
- A 56px strategy identity bar replaces the removed breadcrumb. It names the
  current family and task, then shows combination, executable, account and
  observation facts without adding overview prose.
- Strategy routes now use the centered 1600px content maximum instead of
  full-bleed layout. At the desktop breakpoint the decision surfaces are a
  420px opportunity rail and a 360px execution rail with flexible evidence
  between them. Narrow layouts collapse to two columns and then one column;
  the identity facts remain horizontally available rather than being clipped.
- The resulting chart owns less horizontal area and the opportunity decision,
  historical evidence and execution ticket read as one compact workflow.

### Interaction and verification

- The production menu exposed exactly the four direct strategy tasks and no
  `套利策略` overview item.
- Navigating to `/arbitrage` redirected to `/arbitrage/crypto/basis`.
- Cross-venue funding OI descending moved BTC/USDC (2.6B U) to the first row;
  the sort button announced `按OI (U)排序，当前为降序`.
- The live strategy bar reported Crypto Continuous / 跨所资金费率 and updated
  its observation time. Funding history exposed 24h, 7d and 30d windows.
- No preview or order was submitted during browser verification.
- `npm --prefix web test -- --run`: 21 files and 129 tests passed.
- `npm --prefix web run build`: passed (existing chunk-size advisory only).
- Production web image rebuilt and restarted successfully.
- `git diff --check`: passed.

No P0, P1 or P2 issue remains from the independent review or this layout pass.

## Two-level opportunity rows — 2026-08-21

- Baseline: `docs/design/arbitrage/audit-context-layout-final.png`.
- Final live-data state: `docs/design/arbitrage/audit-opportunity-two-level-final.png`.
- Before/after comparison:
  `docs/design/arbitrage/qa-opportunity-two-level-before-after.png`.
- Both captures are 1920×873 and use the same production route, desktop
  breakpoint and authenticated account state.

### Findings and resolution

- [P1 resolved] Venue routes, blockers and numeric facts previously competed in
  one six-column row, so long symbols and venue names compressed the edge, APR
  and OI fields. Every opportunity now has a fixed quantitative first line and
  a flexible context second line.
- [P2 resolved] The first line contains only the decision and ranking facts:
  instrument, strategy edge, carry/APR and U-notional OI. Edge, carry/APR and OI
  remain direct, accessible sort controls.
- [P2 resolved] Venue plus leg role moved to the left of the second line and the
  execution state moved to its right. Both can truncate independently without
  displacing or wrapping a live numeric value.
- [P2 resolved] The selected-row background spans both levels, preserving one
  click target and one semantic list item instead of introducing nested cards or
  disclosure controls.

### Interaction and verification

- Live cross-venue funding data rendered the two-level row with long route and
  blocker labels without overlap at the 420px opportunity-rail width.
- OI descending moved BTC/USDC (2.5B U) to the first row and the sort control
  announced `按OI (U)排序，当前为降序`.
- No preview or order was submitted during browser verification.
- `npm --prefix web test -- --run`: 21 files and 129 tests passed.
- `npm --prefix web run build`: passed (existing chunk-size advisory only).
- Production web image rebuilt and restarted successfully.
- `git diff --check`: passed before this record was finalized.

No P0, P1 or P2 issue remains from this refinement.

## Quantity and percentage control — 2026-08-21

- Baseline: `docs/design/arbitrage/audit-opportunity-two-level-final.png`.
- Final disabled state: `docs/design/arbitrage/audit-quantity-percent-final.png`.
- Final active state: `docs/design/arbitrage/audit-quantity-percent-active.png`.
- Before/after comparison:
  `docs/design/arbitrage/qa-quantity-percent-before-after.png`.

### Findings and resolution

- [P1 resolved] The quantity input used the 30px control height while four
  independent percentage buttons used the 24px small-button height. The mixed
  heights made one operation look like unrelated controls.
- [P2 resolved] Quantity remains the primary editable field. The percentage
  actions now occupy one equal-width inset track with the same overall height,
  so the row has one baseline and a clear input-to-shortcut hierarchy.
- [P2 resolved] Clicking a shortcut fills the quantity and exposes a persistent
  neutral selected state through `aria-pressed`. Manual quantity input clears
  the state unless it exactly matches the computed percentage.
- [P2 resolved] Disabled shortcuts remain grouped, and coarse-pointer layouts
  raise the input and group to the shared 44px target height.

### Interaction and verification

- A live spot-perpetual opportunity with Bybit and Hyperliquid accounts enabled
  the shortcuts. Selecting 25% populated the quantity and the browser exposed
  `25%` as active and pressed.
- A funding opportunity missing one account kept all shortcuts disabled without
  breaking alignment or changing the row height.
- No preview or order was submitted during browser verification.
- `npm --prefix web test -- --run`: 21 files and 129 tests passed.
- `npm --prefix web run build`: passed (existing chunk-size advisory only).
- Production web image rebuilt and restarted successfully.
- `git diff --check`: passed before this record was finalized.

No P0, P1 or P2 issue remains from this refinement.

final result: passed
