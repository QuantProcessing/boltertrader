import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { ActivityRoute } from "./Activity";

describe("ActivityRoute", () => {
  it("keeps operational activity and immutable audit evidence separate", async () => {
    render(<ActivityRoute
      activity={[{
        eventId: "activity-1",
        sequence: 1,
        cursor: "cursor-1",
        kind: "manual.command",
        productId: "manual",
        accountId: "a1",
        resourceId: "command-1",
        payload: { summary: "Manual order filled", status: "filled" },
        observedAt: "2026-08-08T00:00:00Z",
      }]}
      audit={[{ auditId: "audit-1", action: "account.rotate", resourceType: "account", resourceId: "a1", outcome: "accepted", requestId: "req-1", occurredAt: "2026-08-08T00:01:00Z" }]}
      onRefresh={() => undefined}
    />);

    expect(screen.getByText("Manual order filled")).toBeInTheDocument();
    expect(screen.getByText("2026-08-08T00:00:00Z")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Audit" }));
    expect(screen.getByText("account.rotate")).toBeInTheDocument();
    expect(screen.getByText("req-1")).toBeInTheDocument();
  });
});
