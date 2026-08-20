import { readFileSync } from "node:fs";
import { resolve as resolvePath } from "node:path";

import { describe, expect, it } from "vitest";

/* ---------------------------------------------------------------------------
 * design.md §9, as arithmetic.
 *
 * The diagram in §9 states the terminal's three fixed measures. Before this
 * test they were also written in the routes as `w-64`, `w-80` and `h-64`, and
 * the two disagreed: those utilities are multiples of `--spacing`, which §7 sets
 * to 0.22rem, so they resolved to 225px, 281px and 225px. Nothing failed. The
 * book just quietly became 31px narrower than the design says, which is enough
 * that a five-figure price wraps onto a second line inside a 22px row.
 *
 * That is the failure mode this file exists for: a layout that is wrong by an
 * amount no one can see until a number happens to be long enough. So the routes
 * now take named tokens, and the tokens are checked against the document here.
 *
 * The document is parsed rather than restated for the same reason `contrast.ts`
 * parses `theme.css` — a test carrying its own copy of the numbers proves the
 * copy agrees with the code, which is not the claim being made.
 * ------------------------------------------------------------------------- */

const root = resolvePath(process.cwd(), "..");
const THEME = readFileSync(resolvePath(process.cwd(), "src/styles/theme.css"), "utf8");
const DESIGN = readFileSync(resolvePath(root, "design.md"), "utf8");

/** A `--spacing-*` token's px value, as declared in `theme.css`. */
function token(name: string): number {
  const match = THEME.match(new RegExp(`--spacing-${name}:\\s*(\\d+)px`));
  if (!match?.[1]) throw new Error(`--spacing-${name} is not a px token in theme.css`);
  return Number(match[1]);
}

/** The same token's px value as claimed by the §7 table in `design.md`. */
function documented(name: string): number {
  const match = DESIGN.match(new RegExp(`\\| \`--spacing-${name}\` \\| (\\d+)px \\|`));
  if (!match?.[1]) throw new Error(`--spacing-${name} has no row in the design.md §7 table`);
  return Number(match[1]);
}

describe("the layout tokens", () => {
  it.each(["row", "nav", "bar", "depth", "side", "ticket", "deck", "content"])("matches design.md for %s", (name) => {
    expect(token(name)).toBe(documented(name));
  });

  it("is wider than the utility step it replaced", () => {
    // `w-64` against §7's 0.22rem base is 225.28px, and that is the number the
    // routes were carrying. The token exists to be wider than it; if someone
    // "tidies" `--spacing-side` back down toward the old value, the book's price
    // column starts wrapping again and nothing else here would notice.
    //
    // This is deliberately not an arithmetic model of the column — three
    // columns, two gaps, two paddings and a glyph advance is a guess with four
    // places to be wrong, and the browser is the only thing that knows. What is
    // checked in a unit test is the direction; the width itself was measured.
    expect(token("side")).toBeGreaterThan(64 * 3.52);
  });

  it("states the same numbers in the §9 diagram", () => {
    // The diagram is ASCII art, so it is checked for the figures rather than
    // parsed. A diagram that disagrees with its own table is the drift this
    // whole file is about.
    const diagram = DESIGN.slice(DESIGN.indexOf("│ top nav"), DESIGN.indexOf("└──────"));
    expect(diagram).toContain(`${token("side")}px`);
    expect(diagram).toContain(`${token("ticket")}px`);
    expect(diagram).toContain(`${token("nav")}px`);
    expect(diagram).toContain(`${token("bar")}px`);
  });
});
