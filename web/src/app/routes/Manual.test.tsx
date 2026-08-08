import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ManualRoute } from "./Manual";

const baseProps = {
  mode: "test",
  accounts: [{ accountId: "account-1", displayName: "Binance Demo", venue: "binance", status: "active", productIds: ["manual"] }],
  instruments: [{ instrumentId: "BTC-USDT-PERP", symbol: "BTCUSDT", venue: "binance", ready: true }],
  orders: [],
  positions: [],
  preview: null,
  busy: false,
  onPreview: async () => undefined,
  onSubmit: async () => undefined,
  onCancel: async () => undefined,
  onClose: async () => undefined,
  onRefresh: () => undefined,
};

describe("ManualRoute", () => {
  it("shows limit price only for a limit order", async () => {
    render(<ManualRoute {...baseProps} />);
    expect(screen.queryByLabelText("Limit price")).not.toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Order type"), "limit");
    expect(screen.getByLabelText("Limit price")).toBeRequired();
  });

  it("enables submit for a successful blocker-free Manual preview", () => {
    render(<ManualRoute {...baseProps} preview={{ previewId: "manual-preview", blockers: [], warnings: [] }} />);
    expect(screen.getByRole("button", { name: "Submit order" })).toBeEnabled();
  });

  it("offers cancel and safe close only for actionable state", async () => {
    const onCancel = vi.fn(async () => undefined);
    const onClose = vi.fn(async () => undefined);
    const order = { commandId: "command-a", lineageId: "lineage-a", state: "submitted" };
    const position = { lineageId: "lineage-a", productId: "manual", accountId: "account-1", instrumentId: "BTC-USDT-PERP", quantity: "1" };
    render(<ManualRoute {...baseProps} orders={[order]} positions={[position]} onCancel={onCancel} onClose={onClose} />);

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onCancel).toHaveBeenCalledWith(order);
    expect(onClose).toHaveBeenCalledWith(position);
  });

  it("hides cancel and safe close for terminal orders and zero positions", () => {
    const orders = [
      { commandId: "command-filled", lineageId: "lineage-a", state: "filled" },
      { commandId: "command-canceled", lineageId: "lineage-b", state: "canceled" },
    ];
    const positions = [{ lineageId: "lineage-a", productId: "manual", accountId: "account-1", instrumentId: "BTC-USDT-PERP", quantity: "0" }];
    render(<ManualRoute {...baseProps} orders={orders} positions={positions} />);

    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close" })).not.toBeInTheDocument();
  });
});
