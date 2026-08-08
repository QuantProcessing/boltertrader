import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { FundingRoute } from "./Funding";

describe("FundingRoute", () => {
  it("fails closed when the authoritative preview has blockers", () => {
    render(
      <FundingRoute
        mode="test"
        accounts={[
          { accountId: "long-account", displayName: "Long venue", venue: "binance", status: "active", productIds: ["funding"] },
          { accountId: "short-account", displayName: "Short venue", venue: "bybit", status: "active", productIds: ["funding"] },
        ]}
        opportunities={[
          {
            opportunityId: "opp-1",
            canonicalMarketId: "BTC-USDT-PERP",
            longAccountId: "long-account",
            shortAccountId: "short-account",
            longInstrumentId: "BTC-BINANCE-PERP",
            shortInstrumentId: "BTC-BYBIT-PERP",
            longVenue: "binance",
            shortVenue: "bybit",
            longFundingRate: "0.0001",
            shortFundingRate: "0.0003",
            reservationRequirements: [],
          },
        ]}
        positions={[]}
        commands={[]}
        preview={{ previewId: "preview-1", opportunityId: "opp-1", canSubmit: true, blockers: ["ACCOUNT_NOT_READY"], warnings: [], mode: "test", reservationRequirements: [] }}
        busy={false}
        onPreview={async () => undefined}
        onSubmit={async () => undefined}
        onClose={async () => undefined}
        onRefresh={() => undefined}
      />,
    );

    expect(screen.getByText("ACCOUNT NOT READY")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Submit open" })).toBeDisabled();
  });

  it("previews with the accounts bound to the selected opportunity", async () => {
    const onPreview = vi.fn().mockResolvedValue(undefined);
    render(
      <FundingRoute
        mode="test"
        accounts={[
          { accountId: "unrelated", displayName: "Unrelated", venue: "okx", status: "active", productIds: ["funding"] },
          { accountId: "long-authority", displayName: "Long authority", venue: "binance", status: "active", productIds: ["funding"] },
          { accountId: "short-authority", displayName: "Short authority", venue: "bybit", status: "active", productIds: ["funding"] },
        ]}
        opportunities={[{
          opportunityId: "opp-authority",
          canonicalMarketId: "BTC-USDT-PERP",
          longAccountId: "long-authority",
          shortAccountId: "short-authority",
          longInstrumentId: "BTC-BINANCE-PERP",
          shortInstrumentId: "BTC-BYBIT-PERP",
          longVenue: "binance",
          shortVenue: "bybit",
          longFundingRate: "0.0001",
          shortFundingRate: "0.0003",
          reservationRequirements: [],
        }]}
        positions={[]}
        commands={[]}
        preview={null}
        busy={false}
        onPreview={onPreview}
        onSubmit={async () => undefined}
        onClose={async () => undefined}
        onRefresh={() => undefined}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Preview plan" }));

    expect(onPreview).toHaveBeenCalledWith(expect.objectContaining({
      opportunityId: "opp-authority",
      longAccountId: "long-authority",
      shortAccountId: "short-authority",
    }));
    expect(screen.queryByRole("option", { name: "Unrelated" })).not.toBeInTheDocument();
  });
});
