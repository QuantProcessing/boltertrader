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

const okxVenue: Venue = {
  venue: "okx",
  environment: "demo",
  nativeEnvironment: "demo",
  ready: true,
  products: ["funding", "manual"],
  credentialSchema: {
    schemaVersion: 2,
    authKind: "api_key_secret_passphrase",
    fields: [
      { key: "apiKey", label: "API key" },
      { key: "apiSecret", label: "API secret" },
      { key: "passphrase", label: "Passphrase" },
      { key: "subaccount", label: "Subaccount", metadata: true },
    ],
  },
  routeProfiles: [
    { routeId: "spot-usdt", product: "spot", settlementAsset: "USDT", routeProfileId: "okx.spot.usdt.demo.v1", label: "Spot / USDT" },
  ],
};

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
  desiredStateRevision: 3,
  runtimeGeneration: 2,
  credentialVersion: 1,
  runtimeVersion: 4,
  readiness: { ready: true },
  writeReadiness: { ready: true },
  lastAppliedDesiredStateRevision: 3,
  lastAppliedRuntimeGeneration: 2,
  blockedReason: null,
  capabilities: [],
  observedAt: "2026-08-08T10:00:00Z",
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
    readiness: { ready: true, components: { gateway: { ready: true } } },
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
  it("renders page-level account sections without surface framing", () => {
    renderAccounts();

    for (const heading of ["Trading accounts", "Add account", "Capability discovery"]) {
      expect(screen.getByRole("heading", { name: heading }).closest(".surface")).toBeNull();
    }
  });

  it("shows an authoritative mode conflict and denies account creation", () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderAccounts({
      mode: "test",
      venues: [{ ...venues[0], environment: "live", nativeEnvironment: "live" }],
      onCreate,
    });

    expect(screen.getByRole("option", { name: "binance" })).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("Mode conflict: live venue is unavailable in Test mode.");
    expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();
    expect(onCreate).not.toHaveBeenCalled();
  });

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

  it("switches venue credential schemas without retaining values and supports a second same-venue account", async () => {
    const second = { ...account, accountId: "a2", displayName: "Second account" };
    const props = renderAccounts({ accounts: [account, second], venues: [...venues, okxVenue] });

    expect(screen.getByText("2 configured")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("API key"), "must-clear-on-switch");
    await userEvent.selectOptions(screen.getByLabelText("Venue"), "okx:demo");

    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect(screen.getByLabelText("Passphrase")).toBeInTheDocument();
    expect(screen.getByLabelText("Subaccount")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Account name"), "OKX Demo");
    await userEvent.type(screen.getByLabelText("API key"), "okx-key");
    await userEvent.type(screen.getByLabelText("API secret"), "okx-secret");
    await userEvent.type(screen.getByLabelText("Passphrase"), "okx-passphrase");
    await userEvent.type(screen.getByLabelText("Subaccount"), "desk-a");
    await userEvent.click(screen.getByRole("button", { name: "Add account" }));

    await waitFor(() => expect(props.onCreate).toHaveBeenCalledWith(expect.objectContaining({
      venue: "okx",
      credentialBundle: {
        schemaVersion: 2,
        venue: "okx",
        authKind: "api_key_secret_passphrase",
        metadata: { subaccount: "desk-a" },
        secrets: { apiKey: "okx-key", apiSecret: "okx-secret", passphrase: "okx-passphrase" },
      },
    })));
    for (const label of ["API key", "API secret", "Passphrase", "Subaccount"]) expect(screen.getByLabelText(label)).toHaveValue("");
  });

  it("clears typed credential fields even when account creation fails", async () => {
    const props = renderAccounts({ onCreate: vi.fn().mockRejectedValue(new Error("apiSecret=credential-canary")) });
    await userEvent.type(screen.getByLabelText("Account name"), "Binance Demo");
    await userEvent.type(screen.getByLabelText("API key"), "failed-key");
    await userEvent.type(screen.getByLabelText("API secret"), "failed-secret");
    await userEvent.click(screen.getByRole("button", { name: "Add account" }));

    await waitFor(() => expect(props.onCreate).toHaveBeenCalledOnce());
    expect(screen.getByRole("alert")).toHaveTextContent("Sensitive diagnostic redacted");
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

  it("renders every authoritative runtime group with snapshot fences and recovers private gaps", () => {
    const secondRuntime: RuntimeView = {
      ...runtime,
      runtimeVersion: 8,
      desiredStateRevision: 5,
      runtimeGeneration: 4,
      credentialVersion: 3,
      readiness: { ready: false, reason: "STALE_PUBLIC_CONNECTOR" },
      writeReadiness: { ready: false, reason: "PRIVATE_STREAM_GAP" },
      lastAppliedDesiredStateRevision: 4,
      lastAppliedRuntimeGeneration: 3,
      blockedReason: "STALE_PUBLIC_CONNECTOR",
      capabilities: [],
      observedAt: "2026-08-08T10:00:00Z",
      active: {
        desiredStateRevision: 5,
        runtimeGeneration: 4,
        credentialVersion: 3,
        requestedState: "active",
        routes: [{ routeId: "spot-usdc", product: "spot", settlementAsset: "USDC", routeProfileId: "binance.spot.usdc.demo.v1", requestedState: "active", ready: false, reason: "PRIVATE_STREAM_GAP" }],
      },
      pending: undefined,
      retiring: [],
    };
    const { rerender } = render(<AccountsRoute mode="test" accounts={[account]} runtimes={{ a1: [runtime, secondRuntime] }} venues={venues} capabilities={[]} readiness={{ ready: true, components: {} }} busy={false} onCreate={vi.fn()} onAction={vi.fn()} onRefresh={vi.fn()} />);

    expect(screen.getByRole("row", { name: /Active spot-usdc/i })).toHaveTextContent("PRIVATE STREAM GAP");
    expect(screen.getAllByText("desired 5 / runtime 4 / credential 3")).toHaveLength(2);
    expect(screen.getByText("STALE PUBLIC CONNECTOR")).toBeInTheDocument();

    rerender(<AccountsRoute mode="test" accounts={[account]} runtimes={{ a1: [{ ...secondRuntime, readiness: { ready: true }, writeReadiness: { ready: true }, blockedReason: null, active: { ...secondRuntime.active!, routes: [{ ...secondRuntime.active!.routes[0], ready: true, reason: undefined }] } }] }} venues={venues} capabilities={[]} readiness={{ ready: true, components: {} }} busy={false} onCreate={vi.fn()} onAction={vi.fn()} onRefresh={vi.fn()} />);
    expect(screen.getByRole("row", { name: /Active spot-usdc/i })).toHaveTextContent("Ready");
    expect(screen.queryByText("PRIVATE STREAM GAP")).not.toBeInTheDocument();
  });

  it("renders an authoritative snapshot even when it contains no routes", () => {
    renderAccounts({
      accounts: [account],
      runtimes: { a1: [{
        ...runtime,
        active: undefined,
        pending: { desiredStateRevision: 6, runtimeGeneration: 5, credentialVersion: 4, requestedState: "disabled", state: "ready", routes: [] },
        retiring: [],
      }] },
    });

    expect(screen.getByRole("row", { name: "Pending snapshot" })).toHaveTextContent("desired 6 / runtime 5 / credential 4");
    expect(screen.getByRole("row", { name: "Pending snapshot" })).toHaveTextContent("No routes reported");
  });

  it("uses authoritative readiness for an empty inventory", () => {
    const { rerender } = render(<AccountsRoute mode="test" accounts={[]} runtimes={{}} venues={venues} capabilities={[]} readiness={{ ready: false, components: { gateway: { ready: false, reason: "CONTROL_PLANE_UNAVAILABLE" } } }} busy={false} onCreate={vi.fn()} onAction={vi.fn()} onRefresh={vi.fn()} />);

    expect(screen.getByText("Account provisioning is unavailable.")).toBeInTheDocument();
    expect(screen.queryByText("Platform is ready for account provisioning.")).not.toBeInTheDocument();

    rerender(<AccountsRoute mode="test" accounts={[]} runtimes={{}} venues={venues} capabilities={[]} readiness={{ ready: true, components: { gateway: { ready: true } } }} busy={false} onCreate={vi.fn()} onAction={vi.fn()} onRefresh={vi.fn()} />);
    expect(screen.getByText("Platform is ready for account provisioning.")).toBeInTheDocument();
  });

  it("keeps operation failures beside the affected account and preserves blocked removal", async () => {
    const onAction = vi.fn(async (_accountID: string, action: string) => {
      if (action === "remove") throw new Error("ACCOUNT_BLOCKED");
      throw new Error("PRIVATE_STREAM_GAP");
    });
    renderAccounts({ accounts: [account, { ...account, accountId: "a2", displayName: "Second account" }], onAction });

    await userEvent.click(screen.getByRole("button", { name: "Retry validation for Test account" }));
    expect(await screen.findByRole("alert", { name: "Test account operation error" })).toHaveTextContent("PRIVATE STREAM GAP");
    expect(screen.queryByRole("alert", { name: "Second account operation error" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Remove Test account" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm removal" }));
    expect(await screen.findByRole("group", { name: "Remove Test account" })).toBeInTheDocument();
    expect(within(screen.getByRole("group", { name: "Remove Test account" })).getByRole("alert")).toHaveTextContent("ACCOUNT BLOCKED");
  });

  it("redacts sensitive runtime and API diagnostics", async () => {
    const leaked = { ...runtime, blockedReason: "providerPath=/var/lib/secret-store raw secretRef=opaque-canary gatewayToken=token-canary" };
    const onAction = vi.fn().mockRejectedValue(new Error("apiSecret=credential-canary"));
    renderAccounts({ accounts: [account], runtimes: { a1: [leaked] }, onAction });

    expect(document.body).not.toHaveTextContent("opaque-canary");
    expect(document.body).not.toHaveTextContent("token-canary");
    await userEvent.click(screen.getByRole("button", { name: "Drain Test account" }));
    expect(await screen.findByRole("alert", { name: "Test account operation error" })).toHaveTextContent("Sensitive diagnostic redacted");
    expect(document.body).not.toHaveTextContent("credential-canary");
  });

  it("clears every replacement credential when rotation fails", async () => {
    renderAccounts({ accounts: [account], onAction: vi.fn().mockRejectedValue(new Error("ROTATION_VALIDATION_FAILED")) });
    await userEvent.click(screen.getByRole("button", { name: "Rotate Test account credentials" }));
    await userEvent.type(screen.getByLabelText("Replacement API key for Test account"), "failed-rotation-key");
    await userEvent.type(screen.getByLabelText("Replacement API secret for Test account"), "failed-rotation-secret");
    await userEvent.click(screen.getByRole("button", { name: "Submit rotation" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("ROTATION VALIDATION FAILED");
    expect(screen.getByLabelText("Replacement API key for Test account")).toHaveValue("");
    expect(screen.getByLabelText("Replacement API secret for Test account")).toHaveValue("");
    expect(document.body).not.toHaveTextContent("failed-rotation-secret");
  });

  it("edits policy and confirms removal through inline controls", async () => {
    const props = renderAccounts({ accounts: [account] });
    await userEvent.click(screen.getByRole("button", { name: "Edit Test account" }));
    await userEvent.clear(screen.getByLabelText("Account name for Test account"));
    await userEvent.type(screen.getByLabelText("Account name for Test account"), "Primary test");
    await userEvent.click(screen.getByLabelText("Funding product for Test account"));
    await userEvent.click(screen.getByLabelText("Perp / USDT route for Test account"));
    await userEvent.click(screen.getByLabelText("Spot / USDT route for Test account"));
    await userEvent.click(screen.getByRole("button", { name: "Save account" }));
    expect(props.onAction).toHaveBeenCalledWith("a1", "update", { displayName: "Primary test", productPolicy: ["manual", "funding"], routes: [{ routeId: "spot-usdt", product: "spot", settlementAsset: "USDT", routeProfileId: "binance.spot.usdt.demo.v1", requestedState: "active" }, { routeId: "perp-usdt", product: "perp", settlementAsset: "USDT", routeProfileId: "binance.perp.usdt.demo.v1", requestedState: "disabled" }] });

    await userEvent.click(screen.getByRole("button", { name: "Remove Test account" }));
    expect(props.onAction).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Confirm removal" }));
    expect(props.onAction).toHaveBeenLastCalledWith("a1", "remove");
  });
});
