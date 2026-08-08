import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { LoginView } from "./LoginView";

describe("LoginView", () => {
  it("submits one private workspace credential pair", async () => {
    const onLogin = vi.fn().mockResolvedValue(undefined);
    render(<LoginView theme="dark" busy={false} error="" onLogin={onLogin} onThemeToggle={() => undefined} />);

    await userEvent.type(screen.getByLabelText("Email"), "operator@example.test");
    await userEvent.type(screen.getByLabelText("Password"), "correct horse battery staple");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(onLogin).toHaveBeenCalledWith("operator@example.test", "correct horse battery staple");
  });
});
