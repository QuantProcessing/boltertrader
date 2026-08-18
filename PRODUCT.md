# BolterTrader Product

## Register

product

## Users

BolterTrader is a private trading workspace for an experienced operator who repeatedly monitors exchange readiness, evaluates funding opportunities, submits bounded orders, manages accounts, and reconciles positions and PnL. The primary context is a desktop monitor used for focused, information-dense work, with mobile web used for status checks and safe corrective actions.

## Product Purpose

BolterTrader unifies Funding Trading and Manual Trading in one authenticated product over shared account, market-data, order, portfolio, ledger, and audit authority. It should make the exact account, venue, capability, execution state, and economic result visible at every decision point, and must make it unmistakable that every action reaches a real exchange — there is no test environment to fall back to. Success means the user can move from observation to confirmed action without guessing which service or venue owns the truth.

## Brand Personality

Precise, quiet, trustworthy. The interface should feel operational and deliberate, with enough density for comparison and repeated action but no theatrical decoration. Copy is concise, concrete, and explicit about uncertainty or denial.

The product ships one theme: dark. A trading terminal is looked at for hours in a dimmed room, and every surface, chart stroke, and depth bar is tuned for that one background rather than compromised across two. `design.md` is the normative source for colour, type, spacing, and layout; where this document and that one disagree, `design.md` wins on appearance and this one wins on behaviour.

## Anti-references

- Marketing landing pages, oversized hero typography, and explanatory feature cards inside the authenticated product.
- Card mosaics, nested cards, glassmorphism, and decorative dashboards that fragment related trading facts.
- Professional terminals that expose unsupported controls or imply certainty before authoritative confirmation.
- Product modules that duplicate account, credential, instrument, or market-data controls already owned by the shared platform.

## Design Principles

1. Authority before optimism: show confirmed server state and label provisional, stale, degraded, uncertain, blocked, and manual-intervention states distinctly.
2. Context travels with every action: account, venue environment, instrument, side, quantity, notional, product owner, and warnings stay visible through preview and confirmation.
3. Dense but stable: favor scan-friendly tables, aligned data, and predictable navigation while preventing streaming values or long labels from shifting the layout.
4. Shared controls stay shared: authentication, account security, capability discovery, portfolio, ledger, and audit use one vocabulary across product routes.
5. Safety remains actionable: denial and failure states explain the smallest safe next action without exposing secrets or offering browser controls that weaken trusted mode.

## Accessibility & Inclusion

Target WCAG 2.2 AA. All workflows must be keyboard accessible, provide visible focus, use color plus text or icon for state, preserve useful contrast on every surface of the dark theme, support reduced motion, and maintain at least 44px touch targets on mobile. Dynamic status changes must be announced without stealing focus. Credential values are write-only and must disappear from the rendered interface, browser storage, and subsequent responses after submission.
