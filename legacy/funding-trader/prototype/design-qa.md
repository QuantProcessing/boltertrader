# Design QA — Dark / Light 主题切换

## Comparison Target

- Source visual truth: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-workspace-dark.png`
- Implementation screenshot: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-workspace-light.png`
- Login evidence: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-login-dark.png` and `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-login-light.png`
- Settings evidence: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-settings-light.png`
- User management evidence: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-users-light.png`
- Modal evidence: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-modal-light.png`
- Full-view comparison: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-comparison.png`
- Focused upper-workspace comparison: `C:\Users\Xicod\Documents\funding-trader\prototype\design-qa\theme-comparison-focused.png`
- Viewport: `1280 × 720`, desktop
- State: trader account logged in, “全部 5” selected, BTC-USDT selected, no modal or toast open for the primary comparison

## Findings

No actionable P0, P1, or P2 findings remain.

- Fonts and typography: both themes retain the same compact type stack, weights, line heights, tabular numerals, wrapping, and hierarchy. Light changes foreground tokens only; dense labels remain scannable and no text truncation or density drift was introduced.
- Spacing and layout rhythm: the header, `1 : 3 : 2` upper workbench, tables, row heights, borders, radii, and gaps are identical across the paired captures. The rendered document is `1265 px` wide inside a `1280 px` viewport with no horizontal overflow.
- Colors and visual tokens: Light uses a neutral blue-gray page, white panels, slate borders, and dark text. Green positive values, amber warnings, red critical states, blue selection, disabled controls, and the selected-row gradient remain distinct without importing dark surface fills.
- Image quality and assets: the target has no product imagery or decorative asset requirement. Existing canvas sparklines remain sharp and legible; no source asset was replaced by CSS art, emoji, handcrafted SVG, or placeholder imagery.
- Copy and content: the theme control exposes the target action as “切换为浅色主题 / 切换为深色主题.” All app-specific copy and business data remain identical between themes.
- Icons: the terminal direction intentionally relies on text controls, tags, and sparklines. No new icon mismatch or placeholder glyph was introduced.
- Accessibility and behavior: theme controls are semantic buttons on both login and authenticated screens. Choice persists across refresh and logout. Light-theme inputs, disabled states, errors, danger actions, modal overlay, and focus outlines remain visually distinguishable.
- Viewport resilience: no overlap, clipped persistent control, unexpected wrapping, or theme-caused layout shift was observed at the target desktop viewport.

## Focused Region Evidence

`theme-comparison-focused.png` isolates the header and three-column work surface, where typography, column geometry, row rhythm, selected states, filters, disabled controls, and semantic colors are readable at review scale. The focused pass was required because the dense terminal labels are too small to judge reliably from the full-page composite alone.

## Comparison History

### Pass 1 — blocked

- [P2] Several Light-theme controls inherited dark backgrounds, including refresh, “配置交易所,” MAX, and danger actions.
- Impact: the controls read as unintentional dark blocks and weakened the claim that Light was a complete theme.
- Fix applied: expanded Light token overrides for quiet/secondary/amount/danger controls and verified their computed foreground, border, and background colors.

### Pass 2 — passed

- Post-fix evidence: `theme-workspace-light.png`, `theme-settings-light.png`, `theme-users-light.png`, `theme-modal-light.png`, `theme-comparison.png`, and `theme-comparison-focused.png`.
- Primary interactions tested: switch theme on login, log in under Light, switch Light/Dark in the top bar, refresh and verify persistence, open account menu, open exchange settings, open and close the Add API Key modal, log in as an administrator, and open user management.
- Browser console checked after the interaction sequence: no errors or warnings.
- No remaining P0/P1/P2 visual or interaction defects.

## Follow-up Polish

- No P3 polish item is required for the V1 theme scope.

## Implementation Checklist

- [x] Add a complete Light token set without changing layout or density.
- [x] Add accessible theme controls to login and authenticated header.
- [x] Persist the theme locally across refresh and logout.
- [x] Cover workbench, settings, inputs, disabled states, danger actions, dialogs, overlays, and scrollbars.
- [x] Compare Dark and Light at the same viewport and state.
- [x] Verify core interactions, overflow, and browser console output.
- [x] Document the V1 theme behavior and durable visual rule.

final result: passed
