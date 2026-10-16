import { useMemo } from "react";

import { usd } from "@/lib/format";
import { useWorkspace } from "@/state/workspace";
import { Row } from "@/ui/primitives";

const STABLES = new Set(["USDT", "USDC", "USD", "FDUSD", "USDE", "USDT0"]);

/** Stablecoin totals and unrealized PnL of the selected account. */
export function AccountSummary() {
  const { account, live } = useWorkspace();
  const summary = useMemo(() => {
    const balances = live.balances ?? [];
    const positions = live.positions ?? [];
    const stable = balances.filter((item) => STABLES.has(item.asset.toUpperCase()));
    const total = stable.reduce((sum, item) => sum + Number(item.total), 0);
    const available = stable.reduce((sum, item) => sum + Number(item.available), 0);
    const pnl = positions.reduce((sum, item) => sum + Number(item.unrealizedPnl), 0);
    const margin = positions.reduce((sum, item) => sum + (Number(item.margin) || 0), 0);
    return { total, available, pnl, margin };
  }, [live.balances, live.positions]);

  if (!account) return null;
  return (
    <div className="flex-1 bg-surface p-3">
      <div className="mb-2 text-md">Account</div>
      <Row label="Stablecoin Balance" hint="Sum of USDT/USDC-like balances across markets">{usd(summary.total)}</Row>
      <Row label="Available">{usd(summary.available)}</Row>
      <Row label="Unrealized PNL"><span className={summary.pnl >= 0 ? "text-up" : "text-down"}>{usd(summary.pnl)}</span></Row>
      {summary.margin > 0 && <Row label="Position Margin">{usd(summary.margin)}</Row>}
      <Row label="Private Stream">
        <span className={live.status?.connected ? "text-up" : "text-down"}>{live.status ? (live.status.connected ? "Live" : "Reconnecting") : "Connecting"}</span>
      </Row>
    </div>
  );
}
