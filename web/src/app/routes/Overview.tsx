import { AlertTriangle, RefreshCw } from "lucide-react";

import type { Account, Command, Fill, LedgerEntry, Mode, Position } from "../types";
import { formatDecimal } from "../view-models";
import { StatusBadge } from "../../components/StatusBadge";

interface ComponentReadiness { ready?: boolean; reason?: string; state?: string }
interface Readiness { ready: boolean; components?: Record<string, ComponentReadiness> }
interface OverviewProps {
  mode: Mode | string;
  readiness: Readiness;
  accounts: Account[];
  positions: Position[];
  commands: Command[];
  fills: Fill[];
  ledger: LedgerEntry[];
  onRefresh: () => void;
}

const finding = (value: string) => value.replaceAll("_", " ").toUpperCase();

export function OverviewRoute(props: OverviewProps) {
  const uncertain = props.commands.filter((command) => ["uncertain", "manual_intervention"].includes(command.state ?? command.status ?? ""));
  const degraded = Object.entries(props.readiness.components ?? {}).filter(([, value]) => !value.ready);

  return <div className="route-stack">
    <header className="page-heading"><div><p className="eyebrow">Platform / Overview</p><h1>Trading overview</h1></div><button className="button button--secondary" onClick={props.onRefresh}><RefreshCw />Refresh</button></header>
    {!props.readiness.ready && <section className="state-banner state-banner--warning" aria-live="polite"><AlertTriangle /><div><strong>Platform is degraded</strong><span>{degraded.map(([name, value]) => `${name}: ${finding(value.reason ?? value.state ?? "not ready")}`).join(" · ")}</span></div></section>}
    <section className="overview-strip" aria-label="Workspace summary">
      <div><span>Accounts</span><strong>{props.accounts.length} configured</strong><small>{props.accounts.filter((account) => account.status === "active").length} active</small></div>
      <div><span>Positions</span><strong>{props.positions.length} open {props.positions.length === 1 ? "position" : "positions"}</strong><small>Across all products</small></div>
      <div><span>Execution attention</span><strong>{uncertain.length} uncertain {uncertain.length === 1 ? "command" : "commands"}</strong><small>Uncertain or manual intervention</small></div>
      <div><span>Evidence</span><strong>{props.fills.length} fills</strong><small>{props.ledger.length} ledger entries</small></div>
    </section>

    <section className="surface surface--table"><div className="section-heading"><div><h2>Positions</h2><span>{props.positions.length} open</span></div></div><div className="table-scroll"><table><thead><tr><th>Product</th><th>Instrument</th><th>Account</th><th>Quantity</th><th>Entry</th><th>Mark</th><th>Realized</th><th>Unrealized</th></tr></thead><tbody>{props.positions.map((position) => <tr key={`${position.accountId}:${position.lineageId}:${position.instrumentId}`}><td><StatusBadge state={position.productId} label={position.productId} /></td><td>{position.instrumentId}</td><td>{position.accountId}</td><td className="number">{position.quantity}</td><td className="number">{formatDecimal(position.averageEntryPrice)}</td><td className="number">{formatDecimal(position.markPrice)}</td><td className="number">{formatDecimal(position.realizedPnl, position.settlementCurrency)}</td><td className="number">{formatDecimal(position.unrealizedPnl, position.settlementCurrency)}</td></tr>)}</tbody></table></div>{props.positions.length === 0 && <div className="empty-row">No authoritative positions.</div>}</section>

    <div className="overview-columns">
      <section className="surface surface--table"><div className="section-heading"><div><h2>Commands needing attention</h2><span>{uncertain.length} flagged</span></div></div><div className="table-scroll"><table><thead><tr><th>Command</th><th>Product</th><th>State</th></tr></thead><tbody>{uncertain.map((command) => <tr key={command.commandId ?? command.workItemId}><td><code>{command.commandId ?? command.workItemId}</code></td><td>{command.productId}</td><td><StatusBadge state={command.state ?? command.status ?? "uncertain"} /></td></tr>)}</tbody></table></div>{uncertain.length === 0 && <div className="empty-row">No uncertain commands.</div>}</section>
      <section className="surface surface--table"><div className="section-heading"><div><h2>Latest ledger facts</h2><span>{props.ledger.length} entries</span></div></div><div className="table-scroll"><table><thead><tr><th>Kind</th><th>Product</th><th>Amount</th></tr></thead><tbody>{props.ledger.slice(0, 8).map((entry) => <tr key={entry.entryId}><td>{entry.kind.replaceAll("_", " ")}</td><td>{entry.productId}</td><td className="number">{formatDecimal(entry.amount, entry.currency)}</td></tr>)}</tbody></table></div>{props.ledger.length === 0 && <div className="empty-row">No ledger facts yet.</div>}</section>
    </div>
  </div>;
}
