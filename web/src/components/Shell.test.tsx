import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { Shell } from "./Shell";

describe("Shell", () => {
  it("keeps every product route and the authoritative environment visible", async () => {
    const onNavigate = vi.fn();
    render(
      <Shell
        currentPath="/overview"
        mode="test"
        username="operator@example.test"
        ready
        streamState="connected"
        theme="dark"
        onNavigate={onNavigate}
        onThemeToggle={() => undefined}
        onLogout={() => undefined}
      >
        <div>Workspace content</div>
      </Shell>,
    );

    expect(screen.getByText("Test environment")).toBeInTheDocument();
    for (const label of ["Overview", "Funding", "Manual", "Accounts", "Activity", "Settings"]) {
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    }

    await userEvent.click(screen.getByRole("button", { name: "Manual" }));
    expect(onNavigate).toHaveBeenCalledWith("/manual");
  });
});
