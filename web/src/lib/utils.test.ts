import { describe, expect, it } from "vitest";

import { cn } from "./utils";

describe("cn", () => {
  it("keeps a custom colour and a custom font size together", () => {
    // The regression this guards: an unconfigured tailwind-merge groups both
    // under `text-*` and drops the colour, so a rejected order renders in body
    // grey with no error anywhere.
    expect(cn("text-danger", "text-meta")).toBe("text-danger text-meta");
    expect(cn("text-meta", "text-danger")).toBe("text-meta text-danger");
    expect(cn("text-up", "text-metric")).toBe("text-up text-metric");
  });

  it("still overrides within a group", () => {
    expect(cn("text-muted", "text-danger")).toBe("text-danger");
    expect(cn("text-base", "text-title")).toBe("text-title");
    expect(cn("bg-panel", "bg-raised")).toBe("bg-raised");
    expect(cn("border-line", "border-danger")).toBe("border-danger");
  });

  it("does not confuse a border width with a border colour", () => {
    expect(cn("border", "border-danger")).toBe("border border-danger");
  });

  it("resolves the custom spacing and radius scales", () => {
    expect(cn("h-row", "h-bar")).toBe("h-bar");
    expect(cn("h-depth", "h-row")).toBe("h-row");
    expect(cn("w-side", "w-ticket")).toBe("w-ticket");
    expect(cn("h-deck", "h-row")).toBe("h-row");
    expect(cn("rounded-panel", "rounded-control")).toBe("rounded-control");
    expect(cn("rounded-seam", "rounded-modal")).toBe("rounded-modal");
    expect(cn("rounded-card", "rounded-pill")).toBe("rounded-pill");
  });

  it("knows the tokens added since the palette was rewritten", () => {
    // The regression this guards is quiet by construction: a token defined in
    // `theme.css` but missing from the list in `utils.ts` still paints, it just
    // stops overriding — so both classes survive and the cascade picks, which is
    // whichever Tailwind happened to emit last. Every token in `@theme inline`
    // belongs in that list.
    expect(cn("bg-panel", "bg-fill")).toBe("bg-fill");
    expect(cn("text-muted", "text-faint")).toBe("text-faint");
    expect(cn("bg-buy", "bg-sell")).toBe("bg-sell");
    expect(cn("text-buy-fg", "text-sell-fg")).toBe("text-sell-fg");
    expect(cn("border-line", "border-fill-strong")).toBe("border-fill-strong");
  });

  it("applies conditional class values", () => {
    expect(cn("text-base", false && "text-danger", undefined, "font-medium")).toBe(
      "text-base font-medium",
    );
  });
});
