import { readFileSync } from "node:fs";
import { resolve as resolvePath } from "node:path";

import { describe, expect, it } from "vitest";

/* ---------------------------------------------------------------------------
 * design.md §11.1, as arithmetic.
 *
 * The rule is "WCAG 2.2 AA, verified by arithmetic against the lightest surface
 * a token can land on — not by eye". Written down, that rule is a paragraph
 * someone has to remember while picking a colour; here it is a failing test.
 *
 * It caught the case it was written for. `--bt-sell` is a bright red, and white
 * on it looks like the obvious label — it is what a filled destructive button
 * has everywhere else. It measures 3.16:1. Nothing about the screen says so.
 *
 * The tokens are read out of `theme.css` rather than restated, because a test
 * holding its own copy of the palette proves the copy is accessible and not the
 * product. jsdom does not implement `oklch()` — `getComputedStyle` hands back
 * the string unparsed — so the conversion below is the real one, checked
 * against Chrome's own rendering of the same tokens:
 *
 *   --bt-buy    → #36de95      --bt-sell   → #f75b68
 *   --bt-buy-fg → #002811      buy pair    → 9.18:1
 * ------------------------------------------------------------------------- */

// From the project root, not `import.meta.url`: these tests run under jsdom,
// where that is rewritten to an http URL and cannot be resolved to a file.
const THEME = readFileSync(resolvePath(process.cwd(), "src/styles/theme.css"), "utf8");

/** Every `--bt-*: oklch(...)` in the file, by name. Alpha forms are skipped. */
function palette(): Map<string, [number, number, number]> {
  const found = new Map<string, [number, number, number]>();
  const pattern = /(--bt-[\w-]+):\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)/g;
  for (const match of THEME.matchAll(pattern)) {
    const [, name, l, c, h] = match;
    if (name === undefined || l === undefined || c === undefined || h === undefined) continue;
    found.set(name, [Number(l), Number(c), Number(h)]);
  }
  return found;
}

/** Linear-light channel → 8-bit sRGB. */
function encode(v: number): number {
  const encoded = v <= 0.0031308 ? 12.92 * v : 1.055 * Math.abs(v) ** (1 / 2.4) - 0.055;
  return Math.min(255, Math.max(0, Math.round(encoded * 255)));
}

/** oklch → oklab → linear sRGB → sRGB, per CSS Color 4. */
function toSrgb([l, c, h]: [number, number, number]): [number, number, number] {
  const hr = (h * Math.PI) / 180;
  const [okL, okA, okB] = [l, c * Math.cos(hr), c * Math.sin(hr)];

  const lp = okL + 0.3963377774 * okA + 0.2158037573 * okB;
  const mp = okL - 0.1055613458 * okA - 0.0638541728 * okB;
  const sp = okL - 0.0894841775 * okA - 1.291485548 * okB;
  const [L, M, S] = [lp ** 3, mp ** 3, sp ** 3];

  // Written out rather than mapped, because `.map` over a tuple widens it to
  // `number[]` and the three channels stop being three.
  return [
    encode(4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S),
    encode(-1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S),
    encode(-0.0041960863 * L - 0.7034186147 * M + 1.707614701 * S),
  ];
}

function relativeLuminance([r, g, b]: [number, number, number]): number {
  const channel = (v: number): number => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(a: string, b: string): number {
  const tokens = palette();
  const resolve = (name: string): [number, number, number] => {
    const token = tokens.get(name);
    if (!token) throw new Error(`${name} is not an oklch token in theme.css`);
    return toSrgb(token);
  };
  const [first, second] = [relativeLuminance(resolve(a)), relativeLuminance(resolve(b))];
  const [high, low] = first >= second ? [first, second] : [second, first];
  return (high + 0.05) / (low + 0.05);
}

const hex = (name: string): string => {
  const token = palette().get(name);
  if (!token) throw new Error(`${name} is not an oklch token in theme.css`);
  return `#${toSrgb(token)
    .map((v) => v.toString(16).padStart(2, "0"))
    .join("")}`;
};

describe("the palette's contrast floor", () => {
  it("converts oklch the way the browser does", () => {
    // Ground truth, read out of Chrome's own painted pixels for these tokens.
    // Without this the rest of the file could be confidently wrong.
    expect(hex("--bt-buy")).toBe("#36de95");
    expect(hex("--bt-sell")).toBe("#f75b68");
    expect(hex("--bt-buy-fg")).toBe("#002811");
  });

  // A label drawn *on* a fill, which is the whole reason these pairs exist.
  it.each([
    ["--bt-buy-fg", "--bt-buy"],
    ["--bt-sell-fg", "--bt-sell"],
    ["--bt-accent-fg", "--bt-accent"],
  ])("clears AA for %s on %s", (label, fill) => {
    expect(contrast(label, fill)).toBeGreaterThanOrEqual(4.5);
  });

  // The text ladder, against `raised` — the lightest surface it lands on, which
  // is what §11.1 means by measuring against the worst case rather than `bg`.
  it.each([
    ["--bt-text", 15],
    ["--bt-muted", 7],
    ["--bt-dim", 4.5],
  ])("keeps %s readable on raised", (token, floor) => {
    expect(contrast(token, "--bt-raised")).toBeGreaterThanOrEqual(floor);
  });

  it("keeps faint below the floor, because it must never carry text", () => {
    // Not an accident to be fixed: §11.3. If someone "corrects" this to pass,
    // the token has lost the one property that distinguishes it from `dim`.
    expect(contrast("--bt-faint", "--bt-raised")).toBeLessThan(4.5);
  });
});
