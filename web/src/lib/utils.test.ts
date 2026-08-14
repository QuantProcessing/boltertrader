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
    expect(cn("rounded-md", "rounded-sm")).toBe("rounded-sm");
  });

  it("applies conditional class values", () => {
    expect(cn("text-base", false && "text-danger", undefined, "font-medium")).toBe(
      "text-base font-medium",
    );
  });
});
