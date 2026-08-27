import { describe, expect, it } from "vitest";

import type { AccountPositionSnapshot, Command } from "@/api/types";
import { applyOrderOverlays, applyPositionOverlays, recordAccountRealtimeEvent } from "@/features/trade/account-realtime-overlay";

describe("account realtime overlay", () => {
  it("does not let an older REST position overwrite a newer WS entity", () => {
    const instrumentId = "binance:live:perp:USDT:BTC-USDT";
    const snapshot = positionSnapshot("2026-08-26T00:00:01Z", instrumentId, "1", 1);
    expect(recordAccountRealtimeEvent({
      eventId: "event-position-2",
      accountId: "account-a",
      resourceVersion: 2,
      kind: "account.position",
      observedAt: "2026-08-26T00:00:02Z",
      payload: {
        account_id: "account-a",
        route_id: "perp-usdt",
        instrument_id: instrumentId,
        position_slot: "net",
        operation: "upsert",
        position: {
          position_slot: "net",
          side: "buy",
          quantity: "2",
          entry_price: "78000",
          mark_price: "78100",
          unrealized_pnl: "200",
          liquidation_price: { value: "0", valid: true },
          leverage: { value: "2", valid: true },
          margin_used: { value: "39000", valid: true },
        },
      },
    })).toBe("position");

    const overlaid = applyPositionOverlays(snapshot, { accountId: "account-a", instrumentId });
    expect(overlaid.items[0]).toMatchObject({ quantity: "2", resourceVersion: 2, liquidationPrice: undefined });

    const olderRest = applyPositionOverlays(positionSnapshot("2026-08-26T00:00:01.500Z", instrumentId, "1", 1));
    expect(olderRest.items[0]?.quantity).toBe("2");

    const reconciled = applyPositionOverlays(positionSnapshot("2026-08-26T00:00:03Z", instrumentId, "3", 3));
    expect(reconciled.items[0]?.quantity).toBe("3");
  });

  it("maps Gateway order states and keeps the newer WS state over REST", () => {
    const command = order("2026-08-26T00:00:01Z", "submitted");
    expect(recordAccountRealtimeEvent({
      eventId: "event-order-2",
      accountId: "account-a",
      resourceVersion: 2,
      kind: "account.order",
      observedAt: "2026-08-26T00:00:02Z",
      payload: { command_id: command.commandId, account_id: "account-a", state: "filled" },
    })).toBe("order");
    expect(applyOrderOverlays([command])[0]?.state).toBe("filled");
    expect(applyOrderOverlays([order("2026-08-26T00:00:03Z", "failed")])[0]?.state).toBe("failed");
  });
});

function positionSnapshot(verifiedAt: string, instrumentId: string, quantity: string, resourceVersion: number): AccountPositionSnapshot {
  return {
    projectionRevision: resourceVersion,
    verifiedAt,
    summary: { positionCount: 1, accountCount: 1, staleAccountCount: 0, bySettlementAsset: [] },
    accounts: [{
      accountId: "account-a",
      label: "Binance",
      venue: "binance",
      environment: "live",
      status: "ready",
      routeHealth: "live",
      freshness: "live",
      writeReady: true,
      receivedAt: verifiedAt,
    }],
    items: [{
      accountId: "account-a",
      routeId: "perp-usdt",
      instrumentId,
      positionSlot: "net",
      quantity,
      entryPrice: "78000",
      markPrice: "78100",
      unrealizedPnl: "100",
      resourceVersion,
      observedAt: verifiedAt,
    }],
  };
}

function order(updatedAt: string, state: Command["state"]): Command {
  return {
    commandId: "command-a",
    accountId: "account-a",
    lineageId: "lineage-a",
    productId: "manual",
    reservationId: null,
    idempotencyKey: "idempotency-a",
    commandKind: "place",
    state,
    payload: {},
    blockers: [],
    createdAt: "2026-08-26T00:00:00Z",
    updatedAt,
  };
}
