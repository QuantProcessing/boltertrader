import { CircleOff, RefreshCw, X } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";

import type { Account, Command, Instrument, Mode, Position, Preview } from "../types";
import { formatDecimal } from "../view-models";
import { StatusBadge } from "../../components/StatusBadge";

interface ManualProps {
  mode: Mode | string;
  accounts: Account[];
  instruments: Instrument[];
  orders: Command[];
  positions: Position[];
  preview: Preview | null;
  busy: boolean;
  onPreview: (request: Record<string, unknown>) => Promise<void>;
  onSubmit: (request: Record<string, unknown>) => Promise<void>;
  onCancel: (order: Command) => Promise<void>;
  onClose: (position: Position) => Promise<void>;
  onRefresh: () => void;
}

export function ManualRoute(props: ManualProps) {
  const eligible = useMemo(() => props.accounts.filter((account) => account.status === "active" && account.productIds.includes("manual")), [props.accounts]);
  const [accountID, setAccountID] = useState(eligible[0]?.accountId ?? "");
  const account = props.accounts.find((item) => item.accountId === accountID);
  const instruments = props.instruments.filter((item) => item.ready && (!account || item.venue === account.venue));
  const [instrumentID, setInstrumentID] = useState(instruments[0]?.instrumentId ?? "");
  const [side, setSide] = useState("buy");
  const [orderType, setOrderType] = useState("market");
  const [quantity, setQuantity] = useState("");
  const [limitPrice, setLimitPrice] = useState("");
  const [timeInForce, setTimeInForce] = useState("gtc");
  const [reduceOnly, setReduceOnly] = useState(false);
  const previewReady = Boolean(props.preview && props.preview.blockers.length === 0);

  const preview = async (event: FormEvent) => {
    event.preventDefault();
    await props.onPreview({ accountId: accountID, instrumentId: instrumentID, side, orderType, quantity, limitPrice: orderType === "limit" ? limitPrice : "", timeInForce, reduceOnly, mode: props.mode });
  };

  return <div className="route-stack">
    <header className="page-heading"><div><p className="eyebrow">Product / Manual</p><h1>Manual Trading</h1></div><button className="button button--secondary" onClick={props.onRefresh}><RefreshCw />Refresh</button></header>
    <section className="manual-grid">
      <form className="surface workbench" onSubmit={preview}>
        <div className="section-heading"><div><h2>Order ticket</h2><span>Preview state</span></div><StatusBadge state={props.preview ? (previewReady ? "ready" : "blocked") : "pending"} /></div>
        <label>Account<select value={accountID} onChange={(event) => { setAccountID(event.target.value); setInstrumentID(""); }} required>{eligible.map((item) => <option value={item.accountId} key={item.accountId}>{item.displayName}</option>)}</select></label>
        <label>Instrument<select value={instrumentID} onChange={(event) => setInstrumentID(event.target.value)} required><option value="">Select instrument</option>{instruments.map((item) => <option value={item.instrumentId} key={item.instrumentId}>{item.symbol} · {item.venue}</option>)}</select></label>
        <div className="segmented" role="group" aria-label="Side"><button type="button" className={side === "buy" ? "is-active" : ""} onClick={() => setSide("buy")}>Buy</button><button type="button" className={side === "sell" ? "is-active" : ""} onClick={() => setSide("sell")}>Sell</button></div>
        <div className="form-row"><label>Order type<select value={orderType} onChange={(event) => setOrderType(event.target.value)}><option value="market">Market</option><option value="limit">Limit</option></select></label><label>Time in force<select value={timeInForce} onChange={(event) => setTimeInForce(event.target.value)}><option value="gtc">GTC</option><option value="ioc">IOC</option><option value="fok">FOK</option></select></label></div>
        <label>Quantity<input inputMode="decimal" value={quantity} onChange={(event) => setQuantity(event.target.value)} required /></label>
        {orderType === "limit" && <label>Limit price<input inputMode="decimal" value={limitPrice} onChange={(event) => setLimitPrice(event.target.value)} required /></label>}
        <label className="check-control"><input type="checkbox" checked={reduceOnly} onChange={(event) => setReduceOnly(event.target.checked)} /><span>Reduce only</span></label>
        <button className="button button--secondary button--wide" disabled={props.busy || !accountID || !instrumentID}>Preview order</button>
        {props.preview && <div className="preview-result">{props.preview.blockers.map((item) => <div className="finding finding--danger" key={item}>{item.replaceAll("_", " ").toUpperCase()}</div>)}{props.preview.warnings.map((item) => <div className="finding finding--warning" key={item}>{item.replaceAll("_", " ").toUpperCase()}</div>)}<button type="button" className="button button--primary button--wide" disabled={props.busy || !previewReady} onClick={() => props.onSubmit({ previewId: props.preview?.previewId, action: "place" })}>Submit order</button></div>}
      </form>
      <div className="surface surface--table"><div className="section-heading"><div><h2>Open positions</h2><span>{props.positions.length} open</span></div></div><div className="table-scroll"><table><thead><tr><th>Instrument</th><th>Qty</th><th>Entry</th><th>Mark</th><th>Unrealized</th><th>Action</th></tr></thead><tbody>{props.positions.map((position) => <tr key={`${position.accountId}:${position.instrumentId}:${position.lineageId}`}><td>{position.instrumentId}</td><td className="number">{position.quantity}</td><td className="number">{formatDecimal(position.averageEntryPrice)}</td><td className="number">{formatDecimal(position.markPrice)}</td><td className="number">{formatDecimal(position.unrealizedPnl, position.settlementCurrency)}</td><td>{Number(position.quantity) !== 0 && <button className="button button--danger" disabled={props.busy} onClick={() => props.onClose(position)}><CircleOff />Close</button>}</td></tr>)}</tbody></table></div>{props.positions.length === 0 && <div className="empty-row">No Manual positions.</div>}</div>
    </section>
    <section className="surface surface--table"><div className="section-heading"><div><h2>Orders</h2><span>{props.orders.length} total</span></div></div><div className="table-scroll"><table><thead><tr><th>Order</th><th>Account</th><th>Kind</th><th>State</th><th>Updated</th><th>Action</th></tr></thead><tbody>{props.orders.map((order) => <tr key={order.commandId}><td><code>{order.commandId}</code></td><td>{order.accountId}</td><td>{order.commandKind}</td><td><StatusBadge state={order.state ?? "pending"} /></td><td>{order.updatedAt ?? "-"}</td><td>{order.commandId && ["accepted", "submitted", "uncertain"].includes(order.state ?? "") && <button className="button button--quiet" disabled={props.busy} onClick={() => props.onCancel(order)}><X />Cancel</button>}</td></tr>)}</tbody></table></div></section>
  </div>;
}
