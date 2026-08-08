import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { OverviewRoute } from "./Overview";

describe("OverviewRoute", () => {
  it("shows authoritative counts and exact economic strings", () => {
    render(<OverviewRoute
      mode="test"
      readiness={{ ready: false, components: { gateway: { ready: false, reason: "PRIVATE_STREAM_DEGRADED" } } }}
      accounts={[{ accountId: "a1", displayName: "Test account", venue: "binance", status: "active", productIds: ["manual"] }]}
      positions={[{ lineageId: "l1", productId: "manual", accountId: "a1", instrumentId: "BTCUSDT", quantity: "1", realizedPnl: "1.2300", unrealizedPnl: "4.5600", settlementCurrency: "USDT" }]}
      commands={[{ commandId: "c1", productId: "manual", state: "uncertain" }]}
      fills={[]}
      ledger={[{ entryId: "e1", lineageId: "l1", productId: "manual", accountId: "a1", instrumentId: "BTCUSDT", kind: "funding", amount: "0.001200", currency: "USDT", occurredAt: "2026-08-08T00:00:00Z" }]}
      onRefresh={() => undefined}
    />);

    expect(screen.getByText("1 open position")).toBeInTheDocument();
    expect(screen.getByText("1 uncertain command")).toBeInTheDocument();
    expect(screen.getByText("4.5600 USDT")).toBeInTheDocument();
    expect(screen.getByText("0.001200 USDT")).toBeInTheDocument();
    expect(screen.getByText(/PRIVATE STREAM DEGRADED/)).toBeInTheDocument();
  });
});
