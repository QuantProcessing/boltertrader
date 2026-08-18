# BolterTrader Design

The normative source for how the product looks. `PRODUCT.md` owns behaviour and
principle; this document owns colour, type, spacing, elevation, and layout. Where
they disagree on appearance, this one wins.

Everything here is implemented in `web/src/styles/theme.css` (tokens) and
`web/src/styles/app.css` (base layer). No component may invent a colour, a font
size, or a radius that is not defined here. If something is missing, add it here
first.

---

## 1. Reference and divergence

The visual language is taken from [Lighter](https://app.lighter.xyz): a
near-black canvas, translucent hairlines instead of grey borders, an 11–13px type
floor, semantic radii measured in single pixels, and a trading screen built from
panels rather than cards.

Three deliberate divergences:

1. **Dark only.** Lighter ships a parallel `[data-theme=light]` palette. We do
   not. One background means every surface, chart stroke and depth bar is tuned
   rather than compromised, and it removes a whole axis of contrast arithmetic,
   a `matchMedia` listener, and a class of theme-flip bugs in the imperative
   chart layer.
2. **Contrast is repaired, not copied.** Several of Lighter's own text tokens
   miss WCAG 2.2 AA on their own surfaces — `muted` lands at 3.99:1 on
   `surface`, and `down` at 4.13:1 on `elevated`. Our ladder in §3 is solved so
   that every token that carries text clears 4.5:1 **on the lightest surface it
   can appear on**, not merely on the darkest.
3. **Naming is ours.** Lighter's `dim` is *brighter* than its `meta` and
   `muted`, which reads backwards. Our ladder descends monotonically:
   `text → muted → dim → faint`.

Colour is not required to mean only one thing. The product previously separated
"accent means interactive, semantic means state" and that rule is withdrawn: a
buy button is allowed to be green because it is both the action and the side.
What survives is the accessibility floor, not the taxonomy — **1.4.1 still
applies, so no state may be carried by hue alone.** Every coloured state also
carries a label, a glyph, or a position.

---

## 2. Surfaces

Four steps, all near-black, separated by luminance alone. There is no shadow
between them and no border unless §5 says so.

| Token | OKLCH | Hex | Use |
|---|---|---|---|
| `--bt-bg` | `oklch(0.145 0.012 285)` | `#09090f` | The page. Nothing else. |
| `--bt-panel` | `oklch(0.185 0.012 285)` | `#121218` | Every `Panel`, the top bar, the instrument strip. |
| `--bt-overlay` | `oklch(0.207 0.011 285)` | `#17171d` | Popovers, dialogs, the command palette. |
| `--bt-raised` | `oklch(0.232 0.010 286)` | `#1d1d22` | Row hover, selected row, skeletons, inset wells. |

`raised` is the lightest surface any text sits on, which is why §3 solves every
text ratio against it.

**Elevation is luminance, not shadow.** A panel does not float. Shadow appears
only on things that genuinely leave the plane — popovers, dialogs, toasts — and
only via `--shadow-overlay`.

---

## 3. Text and semantic colour

Solved so the minimum ratio across `bg`, `panel` and `raised` clears the bar. The
"lightest surface" column is the binding one.

| Token | OKLCH | Hex | on `bg` | on `panel` | on `raised` | Verdict |
|---|---|---|---|---|---|---|
| `--bt-text` | `oklch(0.964 0 0)` | `#f3f3f3` | 17.84 | 16.81 | 15.16 | AA text |
| `--bt-muted` | `oklch(0.741 0.005 258)` | `#a9abae` | 8.62 | 8.12 | 7.32 | AA text |
| `--bt-dim` | `oklch(0.622 0.005 286)` | `#86868a` | 5.48 | 5.16 | 4.65 | AA text |
| `--bt-faint` | `oklch(0.436 0.006 286)` | `#515155` | 2.50 | 2.36 | 2.13 | **decorative only** |
| `--bt-up` | `oklch(0.800 0.170 159)` | `#36de95` | 11.35 | 10.69 | 9.64 | AA text |
| `--bt-down` | `oklch(0.680 0.190 19)` | `#f75b68` | 6.28 | 5.92 | 5.34 | AA text |
| `--bt-warn` | `oklch(0.840 0.172 84)` | `#ffbf03` | 11.97 | 11.28 | 10.17 | AA text |
| `--bt-accent` | `oklch(0.780 0.090 271.6)` | `#a3b4f2` | 9.80 | 9.23 | 8.33 | AA text |
| `--bt-danger` | `oklch(0.680 0.195 19)` | `#f95967` | 6.27 | 5.91 | 5.32 | AA text |

`--bt-faint` **must never carry text**. It exists for disabled glyphs, inactive
tick marks, and the un-hovered half of an icon pair. Anything a human has to read
uses `dim` or brighter. `dim` at 4.65:1 on `raised` is the tightest number in the
system; do not darken it.

Remaining state tokens, all at or above `dim`'s luminance so they inherit its
guarantee: `--bt-ok` (= `up`), `--bt-pending` `oklch(0.741 0.005 258)`,
`--bt-manual` `oklch(0.780 0.090 271.6)`.

**Side colours.** Buy and sell get their own pair, because a filled CTA needs a
dark label on a bright field, which the text tokens above cannot supply:

```
--bt-buy:     oklch(0.800 0.170 159)   /* same green as up */
--bt-buy-fg:  oklch(0.235 0.075 160)   /* #002811, 9.18:1 on a buy fill */
--bt-sell:    oklch(0.680 0.190 19)
--bt-sell-fg: oklch(0.235 0.090 19)    /* #3f020c, 5.43:1 on a sell fill */
```

Both labels are dark, because both fills are bright: white on `--bt-sell`
measures **3.16:1** and is the plausible-looking choice rule #1 exists to catch.

The `-fg` suffix is the same one `--bt-accent-fg` carries, and it means the one
thing: a label drawn *on* that fill. `up`/`down` are not interchangeable with
these — they describe which way a number moved and are only ever read as text,
so a control whose selected state is a filled side uses the pair above.

---

## 4. Hairlines and fills

Lighter's structural insight, adopted wholesale: borders and fills are
**white at low alpha**, not opaque greys. One token then reads correctly over
every surface, and a hairline over `raised` is automatically brighter than the
same hairline over `bg` — which is what the eye expects from a real edge.

```
--bt-fill:        oklch(1 0 0 / 0.05)   /* inset wells, track backgrounds */
--bt-fill-strong: oklch(1 0 0 / 0.08)   /* hover on a fill */
--bt-line:        oklch(1 0 0 / 0.07)   /* every default border */
--bt-line-strong: oklch(1 0 0 / 0.14)   /* focused input, active tab, scrollbar thumb */
```

Composited: `line` over `bg` is `#17171d` (1.14:1), over `panel` `#232328`
(1.19:1). That is below the 3:1 non-text bar **on purpose** — a hairline is not a
UI component under 1.4.11, it is a seam. Where a border is the *only* thing
identifying a control (an outline button, a focused field), use
`--bt-line-strong` or the accent ring instead.

Depth bars in the order book use `--bt-up / 0.12` and `--bt-down / 0.12` against
the panel, matching Lighter's row gradients.

---

## 5. Borders, radii, elevation

Radii are semantic, not a t-shirt scale. Lighter's values, kept verbatim,
because they are the reason its density reads as precision rather than as a
cramped Bootstrap:

| Token | Value | Applies to |
|---|---|---|
| `--radius-seam` | 1px | Table cell highlights, depth bars |
| `--radius-control` | 2px | Buttons, inputs, selects, chips |
| `--radius-pill` | 3px | Badges, tags |
| `--radius-card` | 4px | Inset wells |
| `--radius-modal` | 6px | Dialogs, popovers, the palette |
| `--radius-panel` | 8px | `Panel` |

**One border rule: the outer edge only.** A `Panel` draws its border; nothing
inside it draws another. Nested cards are an anti-reference in `PRODUCT.md` and
are enforced here by giving inner containers no border token at all — they
separate with `--bt-fill` or with space.

```
--shadow-overlay: 0 0 0 0.5px oklch(1 0 0 / 0.08),
                  0 1px 2px oklch(0.145 0.012 285 / 0.35),
                  0 4px 8px oklch(0.145 0.012 285 / 0.45);
--shadow-modal:   0 0 0 0.5px oklch(1 0 0 / 0.08),
                  0 1px 2px oklch(0.145 0.012 285 / 0.35),
                  0 4px 8px oklch(0.145 0.012 285 / 0.45),
                  0 16px 32px oklch(0.145 0.012 285 / 0.55);
```

The `0 0 0 0.5px` first layer is a hairline ring, not a glow — it is what keeps a
popover's edge legible where it overlaps a panel of nearly the same luminance.

---

## 6. Type

Inter for prose and labels, Geist Mono for anything numeric. Both are already
installed; the mono face is mandatory wherever a number can change, because
tabular figures are what stop a streaming price from shifting the column.

```
--font-sans: "Inter Variable", "Inter", system-ui, -apple-system, sans-serif;
--font-mono: "Geist Mono Variable", ui-monospace, SFMono-Regular, Menlo, monospace;
```

Five sizes. A sixth would be a decision no one can defend.

| Token | Size / leading | Use |
|---|---|---|
| `--text-micro` | 11px / 14px | `.th` headers, dense chips |
| `--text-meta` | 12px / 16px | Secondary facts, timestamps, units |
| `--text-base` | 13px / 18px | Body, table cells, controls. The default. |
| `--text-title` | 15px / 20px | Panel titles, section headings |
| `--text-metric` | 22px / 26px | The one number a panel exists to show |

Weights use the variable axis, including the non-integer stops Lighter relies on:
`400` body, `485` for a table header or a de-emphasised label, `585` for an
active nav item or a panel title, `700` reserved for `--text-metric`.

Tracking is carried by the size token, not by the call site: `--text-title` and
`--text-metric` each set `--letter-spacing: -0.02em`, so `text-title` alone is
already correct and there is no second class to remember at the eighth heading.
Body and below stay at `0`. `.th` is the one exception and sets `+0.04em` with
uppercase. Numeric cells set `font-variant-numeric: tabular-nums` via
`[data-numeric]`.

---

## 7. Spacing and control sizes

```
--spacing: 0.22rem   /* Tailwind v4 base — 3.52px, tighter than the 4px default */
```

Every `p-2`, `gap-3`, `h-8` in the codebase resolves against this. Adopting
Lighter's tighter base is what lets a 34px row hold 13px text with real padding.

| Token | Value | Use |
|---|---|---|
| `--spacing-row` | 34px | Table rows, list rows |
| `--spacing-bar` | 44px | Top nav, instrument strip. Also the touch-target floor. |
| `--spacing-depth` | 22px | Order-book depth rows — tighter, because the book is only numbers |
| `--spacing-side` | 256px | Order book, spread list — the narrow column |
| `--spacing-ticket` | 320px | The ticket, which holds a form rather than a list |
| `--spacing-deck` | 256px | Blotter, ledger, open pairs — the lower shelf |

The last three are the §9 layout, named so it can only be stated once. They are
absolute rather than steps of the scale for exactly that reason: written as
`w-64`/`w-80` they silently became 225px and 281px the moment `--spacing` moved
to 0.22rem, and 225px is narrow enough that the book's price column wraps
`114 250.19` onto two lines inside a 22px row. A measurement the design fixes
outright should not be a multiple of a base that is free to move.

Control heights, in the semantic scale the components already use:
`sm` 24px, `md` 30px, `lg` 34px (= `--spacing-row`), `icon` 30px square. Under
`pointer-coarse`, every interactive control floors at `--spacing-bar` to satisfy
the 44px requirement in `PRODUCT.md` without inflating the desktop layout.

---

## 8. Motion

```
--duration: 0.15s
--ease-out:    cubic-bezier(0, 0, 0.2, 1)
--ease-in-out: cubic-bezier(0.4, 0, 0.2, 1)
--ease-drawer: cubic-bezier(0.22, 1, 0.36, 1)   /* 0.42s in, 0.18s ease-out back */
```

Motion is for things that enter or leave. A value that changes does not animate —
a price that eases into place is a price you cannot read. Flash-on-change, where
used, is a 1-frame background tint that decays over `--duration`, never a
transform.

`prefers-reduced-motion: reduce` drops every duration to `0.01ms` and disables
the flash entirely. This is already implemented in `app.css` and must stay.

---

## 9. Layout

The shell is `h-dvh`, a flex column, and **the page never scrolls**. Each panel
owns its own overflow. This is what makes the terminal a fixed instrument rather
than a document.

```
┌──────────────────────────────────────────────────────────┐
│ top nav                                    44px, --panel │
├──────────────────────────────────────────────────────────┤
│ instrument strip                           44px, --panel │
├───────────────────────────────┬───────────┬──────────────┤
│ price chart                   │           │              │
│                               │   book    │   ticket     │
│                     flex-1    │   256px   │   320px      │
├───────────────────────────────┤  --side   │  --ticket    │
│ blotter               256px   │           │              │
│                       --deck  │           │              │
└───────────────────────────────┴───────────┴──────────────┘
```

Every number here is a token from §7 rather than a utility step — `h-bar`,
`w-side`, `w-ticket`, `h-deck`. The arbitrage route is the same shape with the
spread list in the book's place, and takes the same three tokens.

Price on the left with the record of what happened beneath it, the live book
beside the price, the ticket at the edge where the hand rests. Operators arrive
already knowing this shape.

Lighter makes its panels a user-rearrangeable `react-grid-layout`. **We do not
adopt that**, and the omission is deliberate: a drag handle on every panel is a
way to break a working layout mid-session, and the arrangement above is already
the one a rearrangeable grid converges on.

---

## 10. Instrument selection

The instrument picker is the one control every session starts with, so it gets a
rule of its own.

Results are **grouped by venue with the group header pinned**, ranked within each
group, and never silently truncated. A flat list filtered by substring and cut at
an arbitrary limit is a defect: typing `btc` must surface Binance, OKX, Bybit and
the rest without a scroll, because "which venue" is precisely the question being
asked.

Ranking within a group, strongest first: exact symbol match, symbol prefix,
symbol substring, then instrument-id substring. Ties break on the catalog's own
order, which is the venue's.

Instruments that are not tradable are **shown and marked**, never hidden. An
operator looking for a market that is halted needs to find it and see why; an
empty result teaches them nothing.

---

## 11. Rules that outrank preference

1. WCAG 2.2 AA, verified by arithmetic against the **lightest** surface a token
   can land on — not by eye, and not against `--bt-bg`.
2. No state is carried by hue alone (1.4.1). Colour always accompanies a label,
   a glyph, or a position.
3. `--bt-faint` never carries text.
4. Borders on the outer edge only. No nested cards.
5. The page does not scroll; panels do.
6. Numbers are mono and tabular, always.
7. Focus is always visible: a 2px `--bt-accent` outline at 2px offset. Never
   removed, never replaced by a colour change alone.
8. Any token renamed here must be mirrored in `web/src/lib/chart-palette.ts`,
   which reads the custom properties off `document.documentElement` by name and
   converts them for `lightweight-charts`. A rename that misses it fails
   silently — the chart falls back to black on black.
