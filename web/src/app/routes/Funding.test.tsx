import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { FundingRoute } from "./Funding";

const account = (accountId: string, displayName: string, venue: string) => ({
  accountId,
  displayName,
  venue,
  mode: "test" as const,
  nativeEnvironment: "demo" as const,
  status: "active" as const,
  productPolicy: ["funding" as const],
  desiredStateRevision: 1,
  runtimeGeneration: 1,
  credentialVersion: 1,
  routes: [],
});

describe("FundingRoute", () => {
  it("fails closed when the authoritative preview has blockers", () => {
    render(
      <FundingRoute
        mode="test"
        accounts={[
          account("long-account", "Long venue", "binance"),
          account("short-account", "Short venue", "bybit"),
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
          account("unrelated", "Unrelated", "okx"),
          account("long-authority", "Long authority", "binance"),
          account("short-authority", "Short authority", "bybit"),
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
