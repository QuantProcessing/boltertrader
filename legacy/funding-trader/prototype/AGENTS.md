# Prototype Instructions

Run the local server yourself and open the preview in the browser available to this environment. Do not give the user server-start instructions when you can run it.

Before making substantial visual changes, use the Product Design plugin's `get-context` skill when the visual source is unclear or no longer matches the current goal. When the user gives durable prototype-specific design feedback, preferences, or decisions, record them in `AGENTS.md`.

When implementing from a selected generated mock, treat that image as the source of truth for layout, component anatomy, density, spacing, color, typography, visible content, and hierarchy.

## Current visual decision

- The user selected ideation direction 2: the dark, high-density quantitative trading terminal.
- Source visual: `C:\Users\Xicod\.codex\generated_images\019f4984-0d5f-7030-8fe8-f3c3e77980e7\exec-712a435a-97e5-4ed0-8c05-e6497be5b83d.png`.
- Verify the desktop composition at `1488 x 1058` and preserve the compact 2:1 opportunity/execution split, small radii, thin borders, and restrained cyan/green/amber/red status system.
- Keep funding-rate alerts, arbitrage opportunities, and the execution workbench in one desktop row, ordered left-to-right with an approximate `1 : 3 : 2` width ratio and minimum usable widths. Render alerts as a vertical severity-tab list at the `token × exchange` level; they remain read-only in V1 and may locate the related opportunity without introducing a separate route.
- Support both Dark and Light themes while preserving the same component anatomy, density, spacing, and layout. Dark remains the default. Light uses a neutral off-white page, white panels, slate borders, dark text, and the same restrained green/amber/red/blue semantic hierarchy.
- Put the theme switch on the login screen and in the authenticated top status area. Persist the choice locally across refreshes and logout; do not require a backend preference or cross-device synchronization in V1.
