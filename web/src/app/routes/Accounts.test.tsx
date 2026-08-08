import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { AccountsRoute } from "./Accounts";

const runtime = {
  accountId: "a1",
  desiredStateRevision: 3,
  runtimeGeneration: 2,
  credentialVersion: 1,
  runtimeVersion: 3,
  readiness: { ready: true },
  writeReadiness: { ready: true },
  lastAppliedDesiredStateRevision: 3,
  lastAppliedRuntimeGeneration: 2,
  blockedReason: null,
  capabilities: [],
  observedAt: "2026-08-08T10:00:00Z",
};

describe("AccountsRoute", () => {
  it("clears write-only credential material immediately after submission", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    render(
      <AccountsRoute
        mode="test"
        accounts={[]}
        runtimes={{}}
        venues={[{ venue: "binance", environment: "demo", ready: true }]}
        capabilities={[]}
        busy={false}
        onCreate={onCreate}
        onAction={async () => undefined}
        onRefresh={() => undefined}
      />,
    );

    await userEvent.type(screen.getByLabelText("Account name"), "Binance Demo");
    fireEvent.change(screen.getByLabelText("Credential JSON"), { target: { value: '{"apiKey":"secret-value"}' } });
    await userEvent.click(screen.getByRole("button", { name: "Add account" }));

    expect(onCreate).toHaveBeenCalledOnce();
    expect(screen.getByLabelText("Credential JSON")).toHaveValue("");
    expect(screen.queryByText("secret-value")).not.toBeInTheDocument();
  });

  it("rotates credentials through a write-only inline control", async () => {
    const onAction = vi.fn().mockResolvedValue(undefined);
    render(
      <AccountsRoute
        mode="test"
        accounts={[{ accountId: "a1", displayName: "Test account", venue: "binance", venueEnvironment: "demo", status: "active", productIds: ["manual"], desiredStateRevision: 3 }]}
        runtimes={{ a1: [runtime] }}
        venues={[{ venue: "binance", environment: "demo", ready: true }]}
        capabilities={[]}
        busy={false}
        onCreate={async () => undefined}
        onAction={onAction}
        onRefresh={() => undefined}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Rotate Test account credentials" }));
    const input = screen.getByLabelText("Replacement credential JSON for Test account");
    fireEvent.change(input, { target: { value: '{"apiKey":"replacement"}' } });
    await userEvent.click(screen.getByRole("button", { name: "Submit rotation" }));

    expect(onAction).toHaveBeenCalledWith("a1", "rotate", { secretMaterial: { apiKey: "replacement" } });
    await waitFor(() => expect(screen.queryByLabelText("Replacement credential JSON for Test account")).not.toBeInTheDocument());
    expect(screen.queryByText("replacement")).not.toBeInTheDocument();
  });

  it("edits account metadata through an inline form", async () => {
    const onAction = vi.fn().mockResolvedValue(undefined);
    render(
      <AccountsRoute
        mode="test"
        accounts={[{ accountId: "a1", displayName: "Test account", venue: "binance", venueEnvironment: "demo", status: "active", productIds: ["manual"], desiredStateRevision: 3 }]}
        runtimes={{ a1: [runtime] }}
        venues={[{ venue: "binance", environment: "demo", ready: true }]}
        capabilities={[]}
        busy={false}
        onCreate={async () => undefined}
        onAction={onAction}
        onRefresh={() => undefined}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Edit Test account" }));
    await userEvent.clear(screen.getByLabelText("Account name for Test account"));
    await userEvent.type(screen.getByLabelText("Account name for Test account"), "Primary test");
    await userEvent.click(screen.getByLabelText("Funding product for Test account"));
    await userEvent.click(screen.getByRole("button", { name: "Save account" }));

    expect(onAction).toHaveBeenCalledWith("a1", "update", { displayName: "Primary test", productIds: ["manual", "funding"] });
    await waitFor(() => expect(screen.queryByLabelText("Account name for Test account")).not.toBeInTheDocument());
  });

  it("does not remove an account until the inline confirmation is accepted", async () => {
    const onAction = vi.fn().mockResolvedValue(undefined);
    render(
      <AccountsRoute
        mode="test"
        accounts={[{ accountId: "a1", displayName: "Test account", venue: "binance", venueEnvironment: "demo", status: "disabled", productIds: ["manual"], desiredStateRevision: 3 }]}
        runtimes={{ a1: [{ ...runtime, readiness: { ready: false, reason: "runtime stopped" }, writeReadiness: { ready: false, reason: "runtime stopped" } }] }}
        venues={[{ venue: "binance", environment: "demo", ready: true }]}
        capabilities={[]}
        busy={false}
        onCreate={async () => undefined}
        onAction={onAction}
        onRefresh={() => undefined}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Remove Test account" }));
    expect(onAction).not.toHaveBeenCalled();
    expect(screen.getByText("Remove Test account?" )).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Confirm removal" }));
    expect(onAction).toHaveBeenCalledWith("a1", "remove");
  });
});
