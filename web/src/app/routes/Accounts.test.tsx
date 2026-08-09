import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Account, RuntimeView, Venue } from "../types";
import { AccountsRoute } from "./Accounts";

const venues: Venue[] = [{
  venue: "binance",
  environment: "demo",
  nativeEnvironment: "demo",
  ready: true,
  products: ["funding", "manual"],
  credentialSchema: {
    schemaVersion: 1,
    authKind: "api_key_secret",
    fields: [
      { key: "apiKey", label: "API key" },
      { key: "apiSecret", label: "API secret" },
    ],
  },
  routeProfiles: [
    { routeId: "spot-usdt", product: "spot", settlementAsset: "USDT", routeProfileId: "binance.spot.usdt.demo.v1", label: "Spot / USDT" },
    { routeId: "perp-usdt", product: "perp", settlementAsset: "USDT", routeProfileId: "binance.perp.usdt.demo.v1", label: "Perp / USDT" },
  ],
}];

const account: Account = {
  accountId: "a1",
  displayName: "Test account",
  venue: "binance",
  mode: "test",
  nativeEnvironment: "demo",
  status: "active",
  productPolicy: ["manual"],
  desiredStateRevision: 3,
  runtimeGeneration: 2,
  credentialVersion: 1,
  routes: [{ routeId: "perp-usdt", product: "perp", settlementAsset: "USDT", routeProfileId: "binance.perp.usdt.demo.v1", requestedState: "active" }],
};

const runtime: RuntimeView = {
  accountId: "a1",
  active: {
    desiredStateRevision: 3,
    runtimeGeneration: 2,
    credentialVersion: 1,
    requestedState: "active",
    routes: [{ routeId: "perp-usdt", product: "perp", settlementAsset: "USDT", routeProfileId: "binance.perp.usdt.demo.v1", requestedState: "active", ready: true }],
  },
  pending: {
    desiredStateRevision: 4,
    runtimeGeneration: 3,
    credentialVersion: 2,
    requestedState: "rotating",
    state: "blocked",
    reason: "validation failed",
    routes: [{ routeId: "spot-usdt", product: "spot", settlementAsset: "USDT", routeProfileId: "binance.spot.usdt.demo.v1", requestedState: "active", ready: false, reason: "private stream gap" }],
  },
  retiring: [{
    desiredStateRevision: 2,
    runtimeGeneration: 1,
    credentialVersion: 1,
    requestedState: "draining",
    routes: [{ routeId: "old-perp", product: "perp", settlementAsset: "USDT", routeProfileId: "binance.perp.usdt.demo.v0", requestedState: "disabled", ready: false, reason: "draining" }],
  }],
};

function renderAccounts(overrides: Partial<React.ComponentProps<typeof AccountsRoute>> = {}) {
  const props: React.ComponentProps<typeof AccountsRoute> = {
    mode: "test",
    accounts: [],
    runtimes: {},
    venues,
    capabilities: [],
    busy: false,
    onCreate: vi.fn().mockResolvedValue(undefined),
    onAction: vi.fn().mockResolvedValue(undefined),
    onRefresh: vi.fn(),
    ...overrides,
  };
  render(<AccountsRoute {...props} />);
  return props;
}

describe("AccountsRoute", () => {
  it("builds a typed v5 account request and clears every credential field", async () => {
    const props = renderAccounts();

    expect(screen.getByText("Native environment").nextSibling).toHaveTextContent("demo");
    expect(screen.queryByLabelText(/environment/i)).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Account name"), "Binance Demo");
    await userEvent.type(screen.getByLabelText("API key"), "browser-key");
    await userEvent.type(screen.getByLabelText("API secret"), "browser-secret");
    await userEvent.click(screen.getByLabelText("Funding product"));
    await userEvent.click(screen.getByLabelText("Spot / USDT route"));
    await userEvent.click(screen.getByRole("button", { name: "Add account" }));

    await waitFor(() => expect(props.onCreate).toHaveBeenCalledWith({
      venue: "binance",
      displayName: "Binance Demo",
      productPolicy: ["funding", "manual"],
      routes: [
        { routeId: "spot-usdt", product: "spot", settlementAsset: "USDT", routeProfileId: "binance.spot.usdt.demo.v1", requestedState: "active" },
        { routeId: "perp-usdt", product: "perp", settlementAsset: "USDT", routeProfileId: "binance.perp.usdt.demo.v1", requestedState: "active" },
      ],
      credentialBundle: {
        schemaVersion: 1,
        venue: "binance",
        authKind: "api_key_secret",
        metadata: {},
        secrets: { apiKey: "browser-key", apiSecret: "browser-secret" },
      },
    }));
    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect(screen.getByLabelText("API secret")).toHaveValue("");
    expect(document.body).not.toHaveTextContent("browser-secret");
  });

  it("clears typed credential fields even when account creation fails", async () => {
    const props = renderAccounts({ onCreate: vi.fn().mockRejectedValue(new Error("ACCOUNT_VALIDATION_FAILED")) });
    await userEvent.type(screen.getByLabelText("Account name"), "Binance Demo");
    await userEvent.type(screen.getByLabelText("API key"), "failed-key");
    await userEvent.type(screen.getByLabelText("API secret"), "failed-secret");
    await userEvent.click(screen.getByRole("button", { name: "Add account" }));

    await waitFor(() => expect(props.onCreate).toHaveBeenCalledOnce());
    expect(screen.getByRole("alert")).toHaveTextContent("ACCOUNT_VALIDATION_FAILED");
    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect(screen.getByLabelText("API secret")).toHaveValue("");
    expect(document.body).not.toHaveTextContent("failed-secret");
  });

  it("rotates credentials through typed write-only fields", async () => {
    const props = renderAccounts({ accounts: [account] });
    await userEvent.click(screen.getByRole("button", { name: "Rotate Test account credentials" }));
    expect(screen.queryByText(/current value/i)).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Replacement API key for Test account"), "replacement-key");
    await userEvent.type(screen.getByLabelText("Replacement API secret for Test account"), "replacement-secret");
    await userEvent.click(screen.getByRole("button", { name: "Submit rotation" }));

    expect(props.onAction).toHaveBeenCalledWith("a1", "rotate", { credentialBundle: {
      schemaVersion: 1,
      venue: "binance",
      authKind: "api_key_secret",
      metadata: {},
      secrets: { apiKey: "replacement-key", apiSecret: "replacement-secret" },
    } });
    await waitFor(() => expect(screen.queryByLabelText("Replacement API secret for Test account")).not.toBeInTheDocument());
    expect(document.body).not.toHaveTextContent("replacement-secret");
  });

  it("renders active, pending, and retiring route diagnostics with retry", async () => {
    const props = renderAccounts({ accounts: [account], runtimes: { a1: [runtime] } });

    for (const state of ["Active", "Pending", "Retiring"]) expect(screen.getByText(state)).toBeInTheDocument();
    const pending = screen.getByRole("row", { name: /Pending spot-usdt/i });
    expect(within(pending).getByText("blocked")).toBeInTheDocument();
    expect(within(pending).getByText("private stream gap")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry validation for Test account" }));
    expect(props.onAction).toHaveBeenCalledWith("a1", "validate");
  });

  it("edits policy and confirms removal through inline controls", async () => {
    const props = renderAccounts({ accounts: [account] });
    await userEvent.click(screen.getByRole("button", { name: "Edit Test account" }));
    await userEvent.clear(screen.getByLabelText("Account name for Test account"));
    await userEvent.type(screen.getByLabelText("Account name for Test account"), "Primary test");
    await userEvent.click(screen.getByLabelText("Funding product for Test account"));
    await userEvent.click(screen.getByRole("button", { name: "Save account" }));
    expect(props.onAction).toHaveBeenCalledWith("a1", "update", { displayName: "Primary test", productPolicy: ["manual", "funding"] });

    await userEvent.click(screen.getByRole("button", { name: "Remove Test account" }));
    expect(props.onAction).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Confirm removal" }));
    expect(props.onAction).toHaveBeenLastCalledWith("a1", "remove");
  });
});
