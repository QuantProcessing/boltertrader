---
name: BolterTrader
description: A precise private control room for multi-product trading.
colors:
  graphite-base: "oklch(0.19 0.006 80)"
  graphite-raised: "oklch(0.225 0.007 80)"
  graphite-soft: "oklch(0.255 0.008 80)"
  graphite-line: "oklch(0.36 0.009 80)"
  graphite-text: "oklch(0.94 0.008 80)"
  graphite-muted: "oklch(0.69 0.012 80)"
  action-blue: "oklch(0.69 0.14 236)"
  action-blue-strong: "oklch(0.74 0.14 236)"
  positive-green: "oklch(0.75 0.14 155)"
  caution-amber: "oklch(0.81 0.14 83)"
  denial-red: "oklch(0.72 0.16 25)"
  light-base: "oklch(0.97 0.006 80)"
  light-raised: "oklch(0.995 0.004 80)"
  light-text: "oklch(0.25 0.012 80)"
typography:
  headline:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1.65rem"
    fontWeight: 690
    lineHeight: 1.2
    letterSpacing: "0"
  title:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 660
    lineHeight: 1.3
    letterSpacing: "0"
  body:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.45
    letterSpacing: "0"
  label:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "0.76rem"
    fontWeight: 650
    lineHeight: 1.3
    letterSpacing: "0"
  numeric:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace"
    fontSize: "0.82rem"
    fontWeight: 400
    lineHeight: 1.45
    letterSpacing: "0"
rounded:
  field: "5px"
  surface: "6px"
  pill: "999px"
spacing:
  tight: "8px"
  control: "13px"
  section: "18px"
  workspace: "30px"
components:
  button-primary:
    backgroundColor: "{colors.action-blue}"
    textColor: "{colors.light-raised}"
    rounded: "{rounded.surface}"
    padding: "0 13px"
    height: "38px"
  button-secondary:
    backgroundColor: "{colors.graphite-raised}"
    textColor: "{colors.graphite-text}"
    rounded: "{rounded.surface}"
    padding: "0 13px"
    height: "38px"
  input:
    backgroundColor: "{colors.graphite-base}"
    textColor: "{colors.graphite-text}"
    rounded: "{rounded.field}"
    padding: "8px 10px"
    height: "40px"
  status-badge:
    backgroundColor: "{colors.graphite-soft}"
    textColor: "{colors.graphite-muted}"
    rounded: "{rounded.pill}"
    padding: "0 8px"
    height: "22px"
---

# Design System: BolterTrader

## Overview

**Creative North Star: "The Private Control Room"**

BolterTrader is a focused operational surface for one experienced trader. It is dense enough for repeated comparison, quiet enough for long sessions, and explicit whenever state is provisional, degraded, denied, or uncertain. Dark is the default for the primary desktop scene; Light is a complete theme, not an afterthought.

Information architecture remains stable across desktop and mobile: one shared shell, shared account authority, and separate product workbenches. Tables carry comparable facts. Workbenches carry inputs and previews. Full-width status bands carry conditions that require attention. Decorative content never competes with trading state.

**Key Characteristics:**

- Restrained graphite surfaces with one muted blue action accent.
- Separate green, amber, and red semantic states that always include text.
- Tight tables, fixed controls, tabular numbers, and predictable navigation.
- Flat-by-default surfaces with borders and tonal layering instead of card mosaics.

## Colors

The palette uses warm graphite neutrals to reduce glare without falling into the familiar blue-black crypto terminal look. The action accent is reserved for the current route, focus, and primary commands.

### Primary

- **Measured Action Blue:** Primary commands, selected navigation, and keyboard focus.

### Secondary

- **Positive Green:** Confirmed readiness and successful terminal outcomes.
- **Caution Amber:** Degraded, recovering, and attention states.
- **Denial Red:** Rejected, failed, manual-intervention, and destructive actions.

### Neutral

- **Warm Graphite Base:** Application background and input wells.
- **Warm Graphite Raised:** Navigation, tables, forms, and stable tool surfaces.
- **Warm Graphite Line:** Dividers, field outlines, and surface boundaries.
- **Graphite Text:** Primary text with muted and faint levels for hierarchy.

**The Ten Percent Rule.** Action blue occupies no more than ten percent of a screen. It indicates selection or agency, never decoration.

**The Semantic Separation Rule.** Success, warning, and danger never reuse action blue and never rely on color alone.

## Typography

**Display Font:** System sans stack
**Body Font:** System sans stack
**Label/Mono Font:** Platform monospace stack for identifiers and numeric facts

**Character:** Familiar operating-system typography keeps the interface fast and trustworthy. Hierarchy comes from size and weight, while all letter spacing remains zero.

### Hierarchy

- **Headline:** Route title only. Never use hero-scale display text inside the authenticated product.
- **Title:** Section and workbench headings.
- **Body:** Operational copy and values; prose is capped near 72 characters.
- **Label:** Inputs, navigation, table headers, and compact state labels.
- **Numeric:** Prices, quantities, PnL, identifiers, and ledger values use tabular figures.

**The Stable Number Rule.** Streaming numeric values use monospace tabular figures and never resize their container.

## Elevation

The system is flat by default. Borders and small tonal shifts establish structure. A single ambient shadow is reserved for the mobile navigation drawer, toast, and login panel because those elements sit above the current task.

### Shadow Vocabulary

- **Ambient Overlay:** A broad, low-opacity shadow for temporary elevated layers only.

**The Flat Surface Rule.** Tables, workbenches, settings bands, and page sections never float with decorative shadows.

## Components

### Buttons

- **Shape:** Compact corners using the surface radius.
- **Primary:** Action blue with light tinted text; only the main command in a workbench uses it.
- **Hover / Focus:** Hover changes the border or tonal fill. Keyboard focus uses a two-pixel visible ring.
- **Secondary / Quiet / Danger:** Secondary is bordered, Quiet is used for row commands, and Danger is red with a full border.

### Chips

- **Style:** Status badges are compact pills with a border, tinted background, uppercase label, and semantic color.
- **State:** Every badge includes a readable state label. Environment identity is a separate chip and is never a browser mode switch.

### Cards / Containers

- **Corner Style:** Small six-pixel radius.
- **Background:** Raised graphite or the matching Light theme surface.
- **Shadow Strategy:** None at rest.
- **Border:** One-pixel neutral divider.
- **Internal Padding:** Varies by function; dense tables and workbenches do not share one universal padding value.

### Inputs / Fields

- **Style:** Dark input well, one-pixel border, five-pixel corners, and at least forty-pixel height.
- **Focus:** Two-pixel action-blue outline with offset.
- **Error / Disabled:** Error copy uses the danger treatment; disabled controls retain shape and reduce opacity.

### Navigation

Desktop uses a fixed 232-pixel side navigation with a sticky top bar. Mobile replaces it with a 44-pixel menu control, an off-canvas drawer, and a dismissible scrim. The selected route uses both color and a visible border.

### Operational Tables

Headers remain compact and uppercase. Rows use stable heights, horizontal scrolling at narrow widths, and tabular numeric alignment. Empty results stay inside the table surface as a concise state, never as a decorative card.

## Do's and Don'ts

### Do:

- **Do** show authoritative server state and keep provisional, stale, degraded, uncertain, blocked, and manual-intervention states distinct.
- **Do** keep account, environment, instrument, quantity, notional, product, and warnings visible at decision points.
- **Do** use one shared vocabulary for authentication, account security, capability discovery, portfolio, ledger, and audit.
- **Do** keep every mobile command at least 44 pixels high and preserve keyboard focus and reduced-motion behavior.

### Don't:

- **Don't** use crypto interfaces dominated by neon-on-black effects, glowing gradients, or speculative visual noise.
- **Don't** build marketing landing pages, oversized hero typography, or explanatory feature cards inside the authenticated product.
- **Don't** use card mosaics, nested cards, glassmorphism, or decorative dashboards that fragment related trading facts.
- **Don't** expose unsupported controls or imply certainty before authoritative confirmation.
- **Don't** duplicate account, credential, instrument, or market-data controls inside product modules.
- **Don't** use colored side-stripe borders, gradient text, decorative motion, or decorative orbs.
