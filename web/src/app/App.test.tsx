import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  delete: vi.fn(),
  clearSession: vi.fn(),
  setSession: vi.fn(),
  onSessionExpired: vi.fn(() => () => undefined),
  snapshot: vi.fn(),
  connectRealtime: vi.fn(() => ({ subscribe: vi.fn(() => () => undefined), getState: vi.fn(() => ({ status: "connected" })), close: vi.fn(), resumeAfterSnapshot: vi.fn() })),
}));

vi.mock("../api/client", () => ({
  apiClient: {
    get: mocks.get,
    post: mocks.post,
    patch: mocks.patch,
    delete: mocks.delete,
    clearSession: mocks.clearSession,
    setSession: mocks.setSession,
    onSessionExpired: mocks.onSessionExpired,
    idempotencyKey: vi.fn(() => "idempotency-test"),
  },
  ApiError: class ApiError extends Error {},
}));
vi.mock("../state/workspaceSnapshot", () => ({ loadWorkspaceSnapshot: mocks.snapshot }));
vi.mock("../state/realtime", () => ({ connectRealtime: mocks.connectRealtime }));

import { App } from "./App";

const session = {
  principal: { id: "principal-1", mode: "test", username: "operator@example.test" },
  session: { csrfToken: "csrf-1", expiresAt: "2026-08-09T00:00:00Z" },
};

const emptySnapshot = {
  status: { ready: true, components: {} }, readiness: { ready: true, components: {} },
  accounts: [], runtimes: {}, venues: [], capabilities: [], instruments: [],
  opportunities: [], commands: [], fills: [], positions: [], ledger: [], activity: [], audit: [], manualOrders: [],
};

const runtime = (accountId: string) => ({
  accountId,
  desiredStateRevision: 7,
  runtimeGeneration: 2,
  credentialVersion: 1,
  runtimeVersion: 3,
  readiness: { ready: true },
  writeReadiness: { ready: true },
  lastAppliedDesiredStateRevision: 7,
  lastAppliedRuntimeGeneration: 2,
  blockedReason: null,
  capabilities: [],
  observedAt: "2026-08-08T10:00:00Z",
});

describe("App", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.onSessionExpired.mockReturnValue(() => undefined);
    mocks.connectRealtime.mockReturnValue({ subscribe: vi.fn(() => () => undefined), getState: vi.fn(() => ({ status: "connected" })), close: vi.fn(), resumeAfterSnapshot: vi.fn() });
  });

  it("fails closed to login when no authenticated session exists", async () => {
    mocks.get.mockRejectedValue(Object.assign(new Error("authentication required"), { status: 401 }));
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Funding" })).not.toBeInTheDocument();
  });

  it("restores the session, loads authoritative state, and navigates products", async () => {
    mocks.get.mockResolvedValue({ data: session, meta: { contractVersion: "v4" } });
    mocks.snapshot.mockResolvedValue(emptySnapshot);
    render(<App />);

    expect(await screen.findByText("Trading overview")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Manual" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Manual Trading" })).toBeInTheDocument());
    expect(window.location.pathname).toBe("/manual");
  });

  it("closes Funding positions with server-issued reservation requirements", async () => {
    const requirements = [
      { accountId: "long", productId: "funding", reservationKind: "close", nettingScope: { accountId: "long", venue: "binance", instrumentId: "BTCUSDT", positionMode: null, settlementScope: null, venueEnvironment: "demo" }, quantity: "1", notional: "100", currency: "USDT", mode: "test" },
      { accountId: "short", productId: "funding", reservationKind: "close", nettingScope: { accountId: "short", venue: "bybit", instrumentId: "BTCUSDT", positionMode: null, settlementScope: null, venueEnvironment: "demo" }, quantity: "1", notional: "100", currency: "USDT", mode: "test" },
    ];
    mocks.get.mockImplementation(async (path: string) => path === "/auth/session" ? { data: session } : { data: { lineageId: "funding-lineage", closeReservationRequirements: requirements } });
    mocks.post.mockResolvedValue({ data: { workItemId: "close-work" } });
    mocks.snapshot.mockResolvedValue({ ...emptySnapshot, positions: [{ lineageId: "funding-lineage", productId: "funding", accountId: "long", instrumentId: "BTCUSDT", quantity: "1" }] });
    render(<App />);

    await userEvent.click(await screen.findByRole("button", { name: "Funding" }));
    await userEvent.click(await screen.findByRole("button", { name: "Close" }));

    await waitFor(() => expect(mocks.post).toHaveBeenCalledWith("/reservations", requirements[0], expect.objectContaining({ idempotencyScope: expect.any(String) })));
    expect(mocks.post).toHaveBeenCalledWith("/reservations", requirements[1], expect.objectContaining({ idempotencyScope: expect.any(String) }));
    expect(mocks.post).toHaveBeenCalledWith("/products/funding/commands", expect.objectContaining({ previewId: "", action: "close", lineageId: "funding-lineage", idempotencyKey: expect.any(String) }), expect.objectContaining({ idempotencyKey: expect.any(String) }));
  });

  it("reserves every preview requirement before submitting a Funding open command", async () => {
    const requirements = [
      { accountId: "long", productId: "funding", reservationKind: "open", nettingScope: { accountId: "long", venue: "binance", instrumentId: "BTC-BINANCE-PERP", positionMode: null, settlementScope: null, venueEnvironment: "demo" }, quantity: "1", notional: "100", currency: "USDT", mode: "test" },
      { accountId: "short", productId: "funding", reservationKind: "open", nettingScope: { accountId: "short", venue: "bybit", instrumentId: "BTC-BYBIT-PERP", positionMode: null, settlementScope: null, venueEnvironment: "demo" }, quantity: "1", notional: "100", currency: "USDT", mode: "test" },
    ];
    mocks.get.mockResolvedValue({ data: session });
    mocks.post.mockImplementation(async (path: string) => {
      if (path === "/products/funding/execution-previews") {
        return { data: { previewId: "preview-open", opportunityId: "opp-1", canSubmit: true, blockers: [], warnings: [], mode: "test", reservationRequirements: requirements } };
      }
      if (path === "/reservations") return { data: { reservationId: "reservation-1", state: "held" } };
      return { data: { workItemId: "funding-work", status: "pending" } };
    });
    mocks.snapshot.mockResolvedValue({
      ...emptySnapshot,
      accounts: [
        { accountId: "long", displayName: "Long", venue: "binance", status: "active", productIds: ["funding"] },
        { accountId: "short", displayName: "Short", venue: "bybit", status: "active", productIds: ["funding"] },
      ],
      opportunities: [{
        opportunityId: "opp-1",
        canonicalMarketId: "BTC-USDT-PERP",
        longAccountId: "long",
        shortAccountId: "short",
        longInstrumentId: "BTC-BINANCE-PERP",
        shortInstrumentId: "BTC-BYBIT-PERP",
        longVenue: "binance",
        shortVenue: "bybit",
        longFundingRate: "0.0001",
        shortFundingRate: "0.0003",
        reservationRequirements: requirements,
      }],
    });
    render(<App />);

    await userEvent.click(await screen.findByRole("button", { name: "Funding" }));
    await userEvent.click(await screen.findByRole("button", { name: "Preview plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Submit open" }));

    await waitFor(() => expect(mocks.post).toHaveBeenCalledWith(
      "/products/funding/commands",
      expect.objectContaining({ previewId: "preview-open", action: "open" }),
      expect.any(Object),
    ));
    const orderedPaths = mocks.post.mock.calls
      .map(([path]) => path)
      .filter((path) => ["/products/funding/execution-previews", "/reservations", "/products/funding/commands"].includes(path));
    expect(orderedPaths).toEqual([
      "/products/funding/execution-previews",
      "/reservations",
      "/reservations",
      "/products/funding/commands",
    ]);
    expect(mocks.post).toHaveBeenCalledWith("/reservations", requirements[0], expect.objectContaining({ idempotencyScope: expect.any(String) }));
    expect(mocks.post).toHaveBeenCalledWith("/reservations", requirements[1], expect.objectContaining({ idempotencyScope: expect.any(String) }));
  });

  it("updates account metadata through the revision-fenced Platform route", async () => {
    const account = { accountId: "account-1", displayName: "Test account", venue: "binance", venueEnvironment: "demo", status: "active", productIds: ["manual"], desiredStateRevision: 7 };
    mocks.get.mockResolvedValue({ data: session });
    mocks.patch.mockResolvedValue({ data: { account } });
    mocks.snapshot.mockResolvedValue({ ...emptySnapshot, accounts: [account], runtimes: { "account-1": [runtime("account-1")] } });
    render(<App />);

    await userEvent.click(await screen.findByRole("button", { name: "Accounts" }));
    await userEvent.click(await screen.findByRole("button", { name: "Edit Test account" }));
    await userEvent.clear(screen.getByLabelText("Account name for Test account"));
    await userEvent.type(screen.getByLabelText("Account name for Test account"), "Primary account");
    await userEvent.click(screen.getByRole("button", { name: "Save account" }));

    await waitFor(() => expect(mocks.patch).toHaveBeenCalledWith(
      "/accounts/account-1",
      { displayName: "Primary account", productIds: ["manual"] },
      expect.objectContaining({ headers: { "If-Match": '"desired-state-revision-7"' }, idempotencyScope: expect.any(String) }),
    ));
  });

  it("removes an account only after confirmation through the revision-fenced Platform route", async () => {
    const account = { accountId: "account-1", displayName: "Test account", venue: "binance", venueEnvironment: "demo", status: "disabled", productIds: ["manual"], desiredStateRevision: 7 };
    mocks.get.mockResolvedValue({ data: session });
    mocks.delete.mockResolvedValue({ data: { account } });
    mocks.snapshot.mockResolvedValue({ ...emptySnapshot, accounts: [account], runtimes: { "account-1": [{ ...runtime("account-1"), readiness: { ready: false, reason: "runtime stopped" }, writeReadiness: { ready: false, reason: "runtime stopped" } }] } });
    render(<App />);

    await userEvent.click(await screen.findByRole("button", { name: "Accounts" }));
    await userEvent.click(await screen.findByRole("button", { name: "Remove Test account" }));
    expect(mocks.delete).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Confirm removal" }));

    await waitFor(() => expect(mocks.delete).toHaveBeenCalledWith(
      "/accounts/account-1",
      expect.objectContaining({ headers: { "If-Match": '"desired-state-revision-7"' }, idempotencyScope: expect.any(String) }),
    ));
  });
});
