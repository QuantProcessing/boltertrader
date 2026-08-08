import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

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
});
