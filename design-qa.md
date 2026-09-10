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
  Trade rows display `BTC-USDT` with `Binance · Perp` context. The canonical
  id remains available as a title and remains unchanged on every API path.
- Spot market strip: ETH-USDT renders inside a 36px two-line selector. Mark,
  open interest, funding and next-funding slots are absent when unpublished;
  the remaining Last, Today, Volume, Open, High and Low facts close the gap.
- Reading width: `/funding` measured exactly 1600px,
  centered at x=160 in a 1920px viewport. `/trade` remains full-bleed.
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

## Chart toolbar and trading rules — 2026-08-28

### Evidence

- Source visual truth:
  `/var/folders/1q/980v33l93gg5zdt__x5lhn2r0000gn/T/codex-clipboard-5ad99a93-8df2-498e-b866-34153dff35b4.png`.
- Approved annotation board:
  <https://www.figma.com/design/fbKQ3bY0QD8OrkQZudc4AN/Untitled?node-id=4-2>.
- Desktop implementation:
  `/private/tmp/bolter-product-audit/implementation-chart-toolbar-final-clean-2762.png`.
- Compact implementation:
  `/private/tmp/bolter-product-audit/implementation-chart-toolbar-1360.png`.
- Rules popover:
  `/private/tmp/bolter-product-audit/implementation-chart-rules-popover.png`.
- Combined full-view comparison:
  `/private/tmp/bolter-product-audit/chart-toolbar-comparison.png`.

### Capture normalization

- Source pixels: 2762 × 1410.
- Requested desktop CSS viewport: 2762 × 1410 at device scale factor 1.
- Browser-rendered implementation pixels: 2551 × 1410; the in-app browser
  surface clamped the available width.
- The comparison pads the implementation on the right to 2762 × 1410 without
  scaling, then stacks source and implementation vertically.
- Compact responsive viewport: 1360 × 900; measured chart width 680px.
- State: Chinese, dark theme, read-only mock public market, Binance BTC-USDT
  perpetual, 15m, rules popover closed for full-view comparison.

### Findings

No actionable P0, P1, or P2 differences remain for the approved scope.

- Fonts and typography: existing Inter and Geist Mono tokens are preserved;
  the rule summary and interval controls use the terminal's existing meta scale
  and numeric treatment.
- Spacing and layout rhythm: trading rules occupy the former top-right interval
  slot; intervals are right-aligned on the OHLC row; duplicate `· 15m` text was
  removed.
- Colors and visual tokens: existing panel, fill, line, text, dim and focus
  tokens are reused. No new palette was introduced.
- Image quality and asset fidelity: no new raster or icon assets are required.
  The chart remains canvas-rendered by the existing chart library.
- Copy and content: the summary shows minimum quantity and minimum order value.
  The popover exposes quantity step and price tick without presenting absent
  rules as zero.
- Responsiveness: at a 760px chart width, the summary and volume hide; at a
  680px chart width, O/H/L also hide. The interval selector stays on one line
  and neither the chart nor document overflows horizontally.
- Accessibility and interaction: the interval selector remains a named radio
  group. The rules trigger is keyboard-focusable; Escape closes the popover.
  Radix supplies expanded state and focus management.

### Focused interaction evidence

- Clicking `5m` changed its `aria-checked` value to `true` and updated the chart
  label to `K 线图，5m 周期`.
- Clicking compact `交易规则` displayed all four available rule rows.
- Pressing Escape closed the popover.
- Browser console error check returned no errors.

### Comparison history

- [P2 resolved] At compact width, the first implementation made the rules
  trigger look like passive text. The final version uses the existing `fill`
  background and `line` border in its resting state, with a stronger hover/open
  border. Post-fix evidence is
  `/private/tmp/bolter-product-audit/implementation-chart-toolbar-1360.png`.

### Follow-up polish

- [P3] Revalidate the container-query thresholds if the fixed widths of the
  order book or ticket change in a later layout revision.

final result: passed

---

## Chart toolbar background and price-scale clearance — 2026-08-28

### Evidence

- Source visual truth:
  `/var/folders/1q/980v33l93gg5zdt__x5lhn2r0000gn/T/codex-clipboard-d14d7a73-bf67-48cf-b63b-a6fe69f5b316.png`.
- Full implementation:
  `/private/tmp/bolter-product-audit/chart-toolbar-background-fix-1920-5m.png`.
- Focused implementation:
  `/private/tmp/bolter-product-audit/chart-toolbar-background-fix-focused-2x.png`.
- Combined focused comparison:
  `/private/tmp/bolter-product-audit/chart-toolbar-background-fix-comparison.png`.

### Capture normalization

- Source pixels: 984 × 430; the source is a high-density crop representing an
  approximately 492 × 215 CSS-pixel region.
- Implementation viewport: 1920 × 1080 CSS pixels at device scale factor 1.
- The implementation was scaled to 2× and cropped to the same 984 × 430 pixel
  region before stacking it with the source. No source pixels were resized.
- State: Chinese, dark theme, read-only mock public market, Binance BTC-USDT
  perpetual, 5m, rules popover closed, keyboard focus cleared.

### Findings

No actionable P0, P1, or P2 difference remains in this correction.

- Fonts and typography: the existing terminal fonts, numeric treatment, sizes
  and weights are unchanged.
- Spacing and layout rhythm: the interval group reserves a 96px chart
  price-scale gutter plus the existing 12px edge inset. The top visible price
  tick is fully readable and does not share horizontal space with the controls.
- Colors and visual tokens: both container-level fills were removed. Only the
  selected interval keeps the existing `raised` token; the rules action is
  transparent at rest and gains `fill` only on hover/open.
- Image quality and asset fidelity: no image or icon assets changed; the chart
  remains rendered by Lightweight Charts.
- Copy and content: trading-rule labels and values are unchanged. The live mock
  reported a different minimum notional than the source capture, which is
  expected changing market/catalog data rather than visual drift.
- Accessibility and interaction: interval radios retain their selected state;
  the rules trigger remains keyboard-focusable and keeps a dashed text
  affordance without a full-width button background.

### Comparison history

- [P2 resolved] The rules summary and the entire interval selector each used a
  filled rounded container, producing two visually heavy stacked pills. The
  resting containers are now transparent and only the active period is raised.
- [P1 resolved] The interval selector occupied the chart's right price-scale
  lane and obscured the top `81400.0` label. A dedicated 96px gutter now keeps
  controls left of every visible price label. Post-fix evidence is the focused
  implementation and combined comparison above.
- [P1 resolved] Repeated 5m/15m switching exposed a late callback from the
  released candle topic, which could feed an older bar into the new series and
  crash the chart. Live candle state now carries instrument and interval
  identity and is ignored outside its active subscription. Four rapid switches
  completed with the chart visible and no new browser errors.

### Verification

- Targeted chart and stream tests: 21 passed.
- Browser interaction: 5m/15m switched four times; the rules popover opened and
  closed; the final interval was restored to 15m.
- Browser logs after a clean reload and the switch sequence: no new errors.
- Full-view overflow: 0px at 1440 × 900 and 1920 × 1080.

final result: passed
