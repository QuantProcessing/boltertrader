import { act, renderHook } from "@testing-library/react";
import { expect, it } from "vitest";
import { localeStore, useT } from "./index";

it("preserves translation dependencies during data ticks and updates on language change", () => {
  const original = localeStore.getSnapshot();
  const { result, rerender, unmount } = renderHook(() => useT());
  const translate = result.current;
  rerender();
  expect(result.current).toBe(translate);
  act(() => localeStore.set(original === "zh" ? "en" : "zh"));
  expect(result.current).not.toBe(translate);
  expect(result.current("nav.trade")).not.toBe(translate("nav.trade"));
  unmount();
  act(() => localeStore.set(original));
});
