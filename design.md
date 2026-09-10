# Bolter personal trading workspace

Updated 2026-09-11. Adapted from [Quiet Workspace](https://gist.github.com/QuantProcessing/1dc0ffb32ce04e92dc966c52c1853ed4), keeping the project's real venue data and simple personal account flow.

## Intent

The workspace answers: which market, what exposure, what happened to the last order, and what can I do next? The chart is context; prices, positions and execution feedback remain readable. Never display an anonymous visitor as having a flat account.

## Surfaces and navigation

Muted green canvas and surfaces, violet navigation and primary actions, green/red directional data with signed values and text. Dark, light and system themes share semantic tokens in `web/src/styles/theme.css`. Canvas charts update when the theme changes. Controls use 8px radii, surfaces 12px, dialogs 16px. Borders identify surfaces without boxing every metric.

A 200px sidebar collapses to 68px; compact viewports use the icon rail. Mobile navigation is a horizontal row with labelled accessible controls. A persistent toolbar carries session, theme, language and preferences.

## Trading layout

Large screens place chart and order book above a shared full-width blotter, with the order ticket to the right. The lower panel receives roughly a third of available height. Depth rows adapt to the actual panel height so both best bid and best ask remain visible. Narrow screens switch chart/book within the same space; mobile stacks the market, ticket and blotter with page scrolling. No viewport width may make order controls unreachable.

The instrument strip prioritizes last trade, mark, index, daily change, volume, open interest and funding. Labels remain 12px; labels on the chart never occupy a large price-scale block. Daily prices and derivative context update from subscriptions. Do not promote infrastructure quota counters into trading metrics.

## Numbers and evidence

Use native currency and explicit units. Linear position PnL is an exact decimal calculation using that instrument's index, entry and signed base quantity. Inverse/unknown contracts retain venue PnL with its source stated. A stale index retains its last estimate and displays “延迟”; it never becomes zero or disappears merely because the stream missed a deadline. Mark and index remain distinct.

Orders appear as soon as the command response arrives. “Submitted” never means “filled”. Persisted Trading order/fill snapshots arrive over the same browser WebSocket as public market and private account topics. Account positions and balances are exchange facts. Reconnection repairs snapshots; it never retries an order write.

## Type, interaction and verification

Inter 14px body, 12px secondary labels, 16px panel headings and approximately 23px page titles; prices use tabular figures and a restrained monospace face. Desktop actions are at least 32–36px; touch controls at least 44px. Keyboard focus must remain visible, dialogs fit the viewport, empty/error/loading/delayed states explain the next action. Color text and filled button contrast are checked for both themes. Responsive geometry is verified in a real browser, not by tests that restate CSS dimensions.

No fake performance metrics, account ACL editor, credential update workflow or deleted arbitrage feature. Account lifecycle remains login → add and verify → use → delete when needed.
