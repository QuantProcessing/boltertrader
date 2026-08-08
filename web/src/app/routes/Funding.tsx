import { RefreshCw, XCircle } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";

import type { Account, Command, FundingOpportunity, FundingPreview, Mode, Position } from "../types";
import { canSubmitPreview, formatDecimal } from "../view-models";
import { StatusBadge } from "../../components/StatusBadge";

interface FundingProps {
  mode: Mode | string;
  accounts: Account[];
  opportunities: FundingOpportunity[];
  positions: Position[];
  commands: Command[];
  preview: FundingPreview | null;
  busy: boolean;
  onPreview: (request: Record<string, unknown>) => Promise<void>;
  onSubmit: (request: Record<string, unknown>) => Promise<void>;
  onClose: (position: Position) => Promise<void>;
  onRefresh: () => void;
}

const displayFinding = (value: string) => value.replaceAll("_", " ").toUpperCase();

export function FundingRoute(props: FundingProps) {
  const [selectedID, setSelectedID] = useState(props.opportunities[0]?.opportunityId ?? "");
  const [notional, setNotional] = useState("100");
  const [slippage, setSlippage] = useState("25");
  const [hold, setHold] = useState(true);
  const selected = props.opportunities.find((item) => item.opportunityId === selectedID) ?? props.opportunities[0];
  const longAccount = props.accounts.find((account) => account.accountId === selected?.longAccountId);
  const shortAccount = props.accounts.find((account) => account.accountId === selected?.shortAccountId);
  const previewMatchesSelection = Boolean(props.preview && selected && props.preview.opportunityId === selected.opportunityId);

  useEffect(() => {
    if (!selectedID && props.opportunities[0]) setSelectedID(props.opportunities[0].opportunityId);
  }, [props.opportunities, selectedID]);

  const preview = async (event: FormEvent) => {
    event.preventDefault();
    if (!selected) return;
    await props.onPreview({
      opportunityId: selected.opportunityId,
      longAccountId: selected.longAccountId,
      shortAccountId: selected.shortAccountId,
      canonicalMarketId: selected.canonicalMarketId,
      notionalPerLeg: notional,
      maxSlippageBps: Number(slippage),
      holdThroughSettlement: hold,
      mode: props.mode,
    });
  };

  return (
    <div className="route-stack">
      <header className="page-heading">
        <div><p className="eyebrow">Product / Funding</p><h1>Funding Trading</h1></div>
        <button className="button button--secondary" onClick={props.onRefresh}><RefreshCw />Refresh</button>
      </header>

      <section className="funding-grid">
        <div className="surface surface--table">
          <div className="section-heading"><div><h2>Opportunities</h2><span>{props.opportunities.length} routes</span></div></div>
          {props.opportunities.length === 0 ? <div className="empty-state"><XCircle /><strong>No authoritative opportunities</strong><span>Check account and market-data readiness.</span></div> : (
            <div className="table-scroll"><table><thead><tr><th>Market</th><th>Long</th><th>Rate</th><th>Short</th><th>Rate</th></tr></thead><tbody>
              {props.opportunities.map((item) => <tr key={item.opportunityId} className={item.opportunityId === selected?.opportunityId ? "is-selected" : ""} onClick={() => setSelectedID(item.opportunityId)}>
                <td><button className="table-select" onClick={() => setSelectedID(item.opportunityId)}>{item.canonicalMarketId}</button></td><td>{item.longVenue}</td><td className="number">{item.longFundingRate}</td><td>{item.shortVenue}</td><td className="number">{item.shortFundingRate}</td>
              </tr>)}
            </tbody></table></div>
          )}
        </div>

        <form className="surface workbench" onSubmit={preview}>
          <div className="section-heading"><div><h2>Execution workbench</h2><span>{selected?.canonicalMarketId ?? "Select a route"}</span></div><StatusBadge state={props.preview ? (previewMatchesSelection && canSubmitPreview(props.preview) ? "ready" : "blocked") : "pending"} /></div>
          <label>Long account<input value={longAccount?.displayName ?? selected?.longAccountId ?? ""} readOnly /></label>
          <label>Short account<input value={shortAccount?.displayName ?? selected?.shortAccountId ?? ""} readOnly /></label>
          <div className="form-row"><label>Notional per leg<input inputMode="decimal" min="0" value={notional} onChange={(event) => setNotional(event.target.value)} required /></label><label>Max slippage (bps)<input type="number" min="0" value={slippage} onChange={(event) => setSlippage(event.target.value)} required /></label></div>
          <label className="check-control"><input type="checkbox" checked={hold} onChange={(event) => setHold(event.target.checked)} /><span>Hold through settlement</span></label>
          <button className="button button--secondary button--wide" type="submit" disabled={props.busy || !selected}>Preview plan</button>
          {props.preview && <div className="preview-result" aria-live="polite">
            <div className="preview-result__meta"><span>Preview</span><code>{props.preview.previewId}</code></div>
            {props.preview.blockers.map((item) => <div className="finding finding--danger" key={item}>{displayFinding(item)}</div>)}
            {props.preview.warnings.map((item) => <div className="finding finding--warning" key={item}>{displayFinding(item)}</div>)}
            <button className="button button--primary button--wide" type="button" disabled={props.busy || !previewMatchesSelection || !canSubmitPreview(props.preview)} onClick={() => props.onSubmit({ previewId: props.preview?.previewId, action: "open" })}>Submit open</button>
          </div>}
        </form>
      </section>

      <section className="surface surface--table"><div className="section-heading"><div><h2>Funding positions</h2><span>{props.positions.length} open</span></div></div><div className="table-scroll"><table><thead><tr><th>Instrument</th><th>Account</th><th>Quantity</th><th>Entry</th><th>Mark</th><th>Unrealized</th><th /></tr></thead><tbody>
        {props.positions.map((position) => <tr key={`${position.accountId}:${position.lineageId}:${position.instrumentId}`}><td>{position.instrumentId}</td><td>{position.accountId}</td><td className="number">{position.quantity}</td><td className="number">{formatDecimal(position.averageEntryPrice)}</td><td className="number">{formatDecimal(position.markPrice)}</td><td className="number">{formatDecimal(position.unrealizedPnl, position.settlementCurrency)}</td><td><button className="button button--quiet" onClick={() => props.onClose(position)} disabled={props.busy}>Close</button></td></tr>)}
      </tbody></table></div>{props.positions.length === 0 && <div className="empty-row">No open Funding positions.</div>}</section>

      <section className="surface surface--table"><div className="section-heading"><div><h2>Funding commands</h2><span>{props.commands.length} total</span></div></div><div className="table-scroll"><table><thead><tr><th>Command</th><th>Lineage</th><th>State</th><th>Updated</th></tr></thead><tbody>{props.commands.map((command) => <tr key={command.commandId ?? command.workItemId}><td><code>{command.commandId ?? command.workItemId}</code></td><td><code>{command.lineageId}</code></td><td><StatusBadge state={command.state ?? command.status ?? "pending"} /></td><td>{command.updatedAt ?? command.createdAt ?? "-"}</td></tr>)}</tbody></table></div></section>
    </div>
  );
}
