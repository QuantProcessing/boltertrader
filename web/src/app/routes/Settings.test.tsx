import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { SettingsRoute } from "./Settings";

describe("SettingsRoute", () => {
  it("changes visual theme without exposing a product mode switch", async () => {
    const onThemeChange = vi.fn();
    render(<SettingsRoute theme="dark" mode="live" username="operator@example.test" contractVersion="v4" onThemeChange={onThemeChange} />);
    await userEvent.click(screen.getByRole("radio", { name: "Light" }));
    expect(onThemeChange).toHaveBeenCalledWith("light");
    expect(screen.queryByRole("radio", { name: /test/i })).not.toBeInTheDocument();
    expect(screen.getByText("Live")).toBeInTheDocument();
  });
});
