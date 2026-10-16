import { useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { api } from "@/api/client";
import type { Order, Position } from "@/api/types";
import { instrumentName, num, price, time, usd } from "@/lib/format";
import { useInstruments } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { Dialog, PrimaryButton, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { submitOrder } from "./submit";

type Tab = "positions" | "orders" | "fills" | "balances";

export function Blotter() {
  const { live, openOrders, fills, account } = useWorkspace();
  const [tab, setTab] = useState<Tab>("positions");
  const positions = live.positions ?? [];
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <Tabs
        value={tab}
        onChange={setTab}
        options={[
          { value: "balances", label: "Balances" },
          { value: "positions", label: `Positions${positions.length ? ` (${positions.length})` : ""}` },
          { value: "orders", label: `Open Orders${openOrders.length ? ` (${openOrders.length})` : ""}` },
          { value: "fills", label: "Trade History" },
        ]}
      />
      <div className="min-h-0 flex-1 overflow-auto">
        {!account ? (
          <Empty>Connect an account to see positions and orders</Empty>
        ) : tab === "positions" ? (
          <Positions positions={positions} loading={live.positions === undefined} />
        ) : tab === "orders" ? (
          <OpenOrders orders={openOrders} />
        ) : tab === "fills" ? (
          <Fills />
        ) : (
          <Balances />
        )}
      </div>
    </div>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <div className="px-3 py-2.5 text-text">{children}</div>;
}

function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <table className="w-full min-w-[760px] border-collapse">
      <thead className="sticky top-0 bg-surface">
        <tr>{head.map((h, i) => <th key={h + i} className={cx("h-8 px-3 font-normal text-muted", i === 0 ? "text-left" : "text-right")}>{h}</th>)}</tr>
      </thead>
      <tbody className="text-text-2">{children}</tbody>
    </table>
  );
}

const td = "h-7 px-3 text-right whitespace-nowrap";
const tdFirst = "h-7 px-3 text-left whitespace-nowrap";

function useTick() {
  const { data } = useInstruments();
  return (id: string) => data?.find((item) => item.id === id)?.tickSize;
}

function Positions({ positions, loading }: { positions: Position[]; loading: boolean }) {
  const tick = useTick();
  const { setInstrumentId } = useWorkspace();
  const [closing, setClosing] = useState<Position | null>(null);
  if (loading) return <Empty><span className="text-muted">Loading positions…</span></Empty>;
  if (!positions.length) return <Empty>No open positions yet</Empty>;
  return (
    <>
      <Table head={["Coin", "Size", "Position Value", "Entry Price", "Mark Price", "PNL (ROE %)", "Liq. Price", "Margin", "Close"]}>
        {positions.map((position) => {
          const value = Number(position.size) * Number(position.markPrice ?? position.entryPrice);
          const pnl = Number(position.unrealizedPnl);
          const margin = Number(position.margin);
          const roe = margin > 0 ? pnl / margin : undefined;
          const color = position.side === "long" ? "text-up" : "text-down";
          return (
            <tr key={position.inst + position.side} className="hover:bg-raised">
              <td className={tdFirst}>
                <button className={cx(color, "hover:underline")} onClick={() => setInstrumentId(position.inst)}>{instrumentName(position.inst)}</button>
                {position.leverage && <span className="ml-1.5 text-muted">{num(position.leverage, 0)}x</span>}
              </td>
              <td className={cx(td, color)}>{(position.side === "short" ? "-" : "") + num(position.size)}</td>
              <td className={td}>{usd(value)}</td>
              <td className={td}>{price(position.entryPrice, tick(position.inst))}</td>
              <td className={td}>{price(position.markPrice, tick(position.inst))}</td>
              <td className={cx(td, pnl >= 0 ? "text-up" : "text-down")}>
                {usd(pnl)}{roe !== undefined && ` (${(roe * 100).toFixed(1)}%)`}
              </td>
              <td className={td}>{price(position.liquidationPrice, tick(position.inst))}</td>
              <td className={td}>{position.margin ? usd(position.margin) : "—"}</td>
              <td className={td}>
                <button className="text-accent hover:underline" onClick={() => setClosing(position)}>Market</button>
              </td>
            </tr>
          );
        })}
      </Table>
      {closing && <ClosePosition position={closing} onDone={() => setClosing(null)} />}
    </>
  );
}

function ClosePosition({ position, onDone }: { position: Position; onDone: () => void }) {
  const { account } = useWorkspace();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const close = async () => {
    if (!account) return;
    setBusy(true);
    const outcome = await submitOrder(account.id, {
      inst: position.inst, side: position.side === "long" ? "sell" : "buy", type: "market", qty: position.size, reduceOnly: true,
    });
    setBusy(false);
    if (outcome.kind === "accepted") {
      toast.push({ kind: "info", title: `Closing ${num(position.size)} ${instrumentName(position.inst)}`, detail: outcome.order.avgPrice ? `Average price ${num(outcome.order.avgPrice)}` : undefined });
      onDone();
    } else {
      toast.push({ kind: "error", title: "Close failed", detail: outcome.message });
    }
  };
  return (
    <Dialog open onOpenChange={(open) => !open && onDone()} title="Market Close" description={`This will close your ${position.side} ${num(position.size)} ${instrumentName(position.inst)} at market with a reduce-only order.`}>
      <PrimaryButton onClick={close} disabled={busy}>{busy ? "Closing…" : "Market Close"}</PrimaryButton>
    </Dialog>
  );
}

function OpenOrders({ orders }: { orders: Order[] }) {
  const tick = useTick();
  const { account } = useWorkspace();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [cancelling, setCancelling] = useState<string | null>(null);
  if (!orders.length) return <Empty>No open orders</Empty>;
  const cancel = async (order: Order) => {
    if (!account) return;
    setCancelling(order.orderId);
    try {
      await api.cancel(account.id, order.inst, order.orderId);
      queryClient.setQueryData<Order[]>(["orders", account.id], (current = []) => current.filter((item) => item.orderId !== order.orderId));
    } catch (error) {
      toast.push({ kind: "error", title: "Cancel failed", detail: (error as Error).message });
    } finally {
      setCancelling(null);
    }
  };
  return (
    <Table head={["Time", "Coin", "Type", "Side", "Price", "Size", "Filled", "Reduce Only", ""]}>
      {orders.map((order) => (
        <tr key={order.orderId} className="hover:bg-raised">
          <td className={tdFirst}>{time(order.createdAt ?? order.updatedAt ?? 0)}</td>
          <td className={td}>{instrumentName(order.inst)}</td>
          <td className={td}>{order.type === "limit" ? "Limit" : "Market"}{order.tif && order.tif !== "gtc" ? ` · ${order.tif.toUpperCase()}` : ""}</td>
          <td className={cx(td, order.side === "buy" ? "text-up" : "text-down")}>{order.side === "buy" ? "Buy" : "Sell"}</td>
          <td className={td}>{price(order.price, tick(order.inst))}</td>
          <td className={td}>{num(order.qty)}</td>
          <td className={td}>{num(order.filled)}</td>
          <td className={td}>{order.reduceOnly ? "Yes" : "No"}</td>
          <td className={td}>
            <button className="text-accent hover:underline disabled:text-muted" disabled={cancelling === order.orderId} onClick={() => cancel(order)}>
              {cancelling === order.orderId ? "Cancelling…" : "Cancel"}
            </button>
          </td>
        </tr>
      ))}
    </Table>
  );
}

function Fills() {
  const { fills } = useWorkspace();
  const tick = useTick();
  if (!fills.length) return <Empty>No trades yet</Empty>;
  return (
    <Table head={["Time", "Coin", "Side", "Price", "Size", "Trade Value", "Fee"]}>
      {fills.map((fill) => (
        <tr key={fill.fillId} className="hover:bg-raised">
          <td className={tdFirst}>{time(fill.ts)}</td>
          <td className={td}>{instrumentName(fill.inst)}</td>
          <td className={cx(td, fill.side === "buy" ? "text-up" : "text-down")}>{fill.side === "buy" ? "Buy" : "Sell"}</td>
          <td className={td}>{price(fill.price, tick(fill.inst))}</td>
          <td className={td}>{num(fill.qty)}</td>
          <td className={td}>{usd(Number(fill.price) * Number(fill.qty))}</td>
          <td className={td}>{num(fill.fee)} {fill.feeAsset}</td>
        </tr>
      ))}
    </Table>
  );
}

function Balances() {
  const { live } = useWorkspace();
  const balances = live.balances;
  if (balances === undefined) return <Empty><span className="text-muted">Loading balances…</span></Empty>;
  if (!balances.length) return <Empty>No balances</Empty>;
  return (
    <Table head={["Coin", "Market", "Total", "Available", "In Use"]}>
      {balances.map((balance) => (
        <tr key={balance.market + balance.asset} className="hover:bg-raised">
          <td className={tdFirst}>{balance.asset}</td>
          <td className={td}>{balance.market}</td>
          <td className={td}>{num(balance.total)}</td>
          <td className={td}>{num(balance.available)}</td>
          <td className={td}>{num(balance.locked)}</td>
        </tr>
      ))}
    </Table>
  );
}
