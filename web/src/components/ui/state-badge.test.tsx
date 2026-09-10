import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { StateBadge } from "./state-badge";

describe("StateBadge", () => {
  it("shows its detail as a stable hover tooltip", async () => {
    const user = userEvent.setup();
    render(<StateBadge tone="danger" label="Failed" detail="Insufficient margin" />);

    await user.hover(screen.getByText("Failed"));

    expect(await screen.findByRole("tooltip")).toHaveTextContent("Insufficient margin");
  });
});
