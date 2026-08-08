import { KeyRound, Plus, RefreshCw, ShieldCheck } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";

import type { Account, Capability, Mode, RuntimeView, Venue } from "../types";
import { runtimeDisplay } from "../view-models";
import { StatusBadge } from "../../components/StatusBadge";

interface AccountsProps {
  mode: Mode | string;
  accounts: Account[];
  runtimes: Record<string, RuntimeView[]>;
  venues: Venue[];
  capabilities: Capability[];
  busy: boolean;
  onCreate: (request: Record<string, unknown>) => Promise<void>;
  onAction: (accountID: string, action: string, payload?: Record<string, unknown>) => Promise<void>;
  onRefresh: () => void;
}

export function AccountsRoute(props: AccountsProps) {
  const available = useMemo(() => props.venues.filter((venue) => venue.ready && (props.mode === "live" ? venue.environment === "live" : venue.environment !== "live")), [props.mode, props.venues]);
  const [venueKey, setVenueKey] = useState(available[0] ? `${available[0].venue}:${available[0].environment}` : "");
  const [displayName, setDisplayName] = useState("");
  const [credential, setCredential] = useState("");
  const [products, setProducts] = useState(["funding", "manual"]);
  const [formError, setFormError] = useState("");
  const [rotatingID, setRotatingID] = useState("");
  const [rotationCredential, setRotationCredential] = useState("");
  const [rotationError, setRotationError] = useState("");
  const [editingID, setEditingID] = useState("");
  const [editDisplayName, setEditDisplayName] = useState("");
  const [editProducts, setEditProducts] = useState<string[]>([]);
  const [editError, setEditError] = useState("");
  const [removingID, setRemovingID] = useState("");

  const create = async (event: FormEvent) => {
    event.preventDefault();
    setFormError("");
    const [venue, venueEnvironment] = venueKey.split(":");
    try {
      const secretMaterial = JSON.parse(credential) as unknown;
      await props.onCreate({ venue, venueEnvironment, displayName, productIds: products, secretMaterial });
      setCredential("");
      setDisplayName("");
    } catch (error) {
      setFormError(error instanceof SyntaxError ? "Credential JSON is invalid." : error instanceof Error ? error.message : "Account creation failed.");
    }
  };

  const toggleProduct = (product: string) => setProducts((current) => current.includes(product) ? current.filter((item) => item !== product) : [...current, product]);
  const rotatingAccount = props.accounts.find((account) => account.accountId === rotatingID);
  const editingAccount = props.accounts.find((account) => account.accountId === editingID);
  const removingAccount = props.accounts.find((account) => account.accountId === removingID);
  const edit = (account: Account) => {
    setEditingID(account.accountId);
    setEditDisplayName(account.displayName);
    setEditProducts([...account.productIds]);
    setEditError("");
    setRotatingID("");
    setRemovingID("");
  };
  const saveEdit = async (event: FormEvent) => {
    event.preventDefault();
    if (!editDisplayName.trim() || editProducts.length === 0) {
      setEditError("Account name and at least one product are required.");
      return;
    }
    await props.onAction(editingID, "update", { displayName: editDisplayName.trim(), productIds: editProducts });
    setEditingID("");
  };
  const toggleEditProduct = (product: string) => setEditProducts((current) => current.includes(product) ? current.filter((item) => item !== product) : [...current, product]);
  const rotate = async (event: FormEvent) => {
    event.preventDefault();
    setRotationError("");
    try {
      const secretMaterial = JSON.parse(rotationCredential) as unknown;
      await props.onAction(rotatingID, "rotate", { secretMaterial });
      setRotationCredential("");
      setRotatingID("");
    } catch (error) {
      setRotationError(error instanceof SyntaxError ? "Replacement credential JSON is invalid." : error instanceof Error ? error.message : "Credential rotation failed.");
    }
  };

  return <div className="route-stack">
    <header className="page-heading"><div><p className="eyebrow">Platform / Security</p><h1>Accounts & Security</h1></div><button className="button button--secondary" onClick={props.onRefresh}><RefreshCw />Refresh</button></header>
    <section className="accounts-layout">
      <div className="surface surface--table"><div className="section-heading"><div><h2>Trading accounts</h2><span>{props.accounts.length} configured</span></div></div><div className="table-scroll"><table><thead><tr><th>Account</th><th>Venue</th><th>Products</th><th>State</th><th>Runtime</th><th>Actions</th></tr></thead><tbody>{props.accounts.map((account) => {
        const runtime = runtimeDisplay(props.runtimes[account.accountId]?.[0]);
        return <tr key={account.accountId}><td><strong>{account.displayName}</strong><small>{account.accountId}</small></td><td>{account.venue}<small>{account.venueEnvironment}</small></td><td>{account.productIds.join(", ")}</td><td><StatusBadge state={account.status} /></td><td><StatusBadge state={runtime.state} />{runtime.reason && <small>{runtime.reason}</small>}</td><td><div className="row-actions"><button className="button button--quiet" aria-label={`Edit ${account.displayName}`} onClick={() => edit(account)} disabled={props.busy}>Edit</button><button className="button button--quiet" onClick={() => props.onAction(account.accountId, "validate")} disabled={props.busy}>Validate</button><button className="button button--quiet" aria-label={`Rotate ${account.displayName} credentials`} onClick={() => { setRotatingID(account.accountId); setRotationCredential(""); setRotationError(""); setEditingID(""); setRemovingID(""); }} disabled={props.busy}>Rotate</button><button className="button button--quiet" onClick={() => props.onAction(account.accountId, "drain")} disabled={props.busy}>Drain</button><button className="button button--quiet" onClick={() => props.onAction(account.accountId, "disable")} disabled={props.busy}>Disable</button><button className="button button--quiet button--danger" aria-label={`Remove ${account.displayName}`} onClick={() => { setRemovingID(account.accountId); setEditingID(""); setRotatingID(""); }} disabled={props.busy}>Remove</button></div></td></tr>;
      })}</tbody></table></div>{props.accounts.length === 0 && <div className="empty-state"><ShieldCheck /><strong>No accounts configured</strong><span>Add a Test account to discover private trading capabilities.</span></div>}
        {editingAccount && <form className="rotation-panel" onSubmit={saveEdit}><div><strong>Edit {editingAccount.displayName}</strong></div><label>{`Account name for ${editingAccount.displayName}`}<input value={editDisplayName} onChange={(event) => setEditDisplayName(event.target.value)} required /></label><fieldset><legend>Products</legend><label className="check-control"><input aria-label={`Funding product for ${editingAccount.displayName}`} type="checkbox" checked={editProducts.includes("funding")} onChange={() => toggleEditProduct("funding")} /><span>Funding</span></label><label className="check-control"><input aria-label={`Manual product for ${editingAccount.displayName}`} type="checkbox" checked={editProducts.includes("manual")} onChange={() => toggleEditProduct("manual")} /><span>Manual</span></label></fieldset>{editError && <div className="finding finding--danger" role="alert">{editError}</div>}<div className="row-actions"><button type="button" className="button button--secondary" onClick={() => setEditingID("")}>Cancel</button><button className="button button--primary" disabled={props.busy}>Save account</button></div></form>}
        {rotatingAccount && <form className="rotation-panel" onSubmit={rotate}><div><strong>Rotate {rotatingAccount.displayName}</strong><span>Current credentials remain active until Platform accepts the replacement.</span></div><label>{`Replacement credential JSON for ${rotatingAccount.displayName}`}<textarea rows={4} autoComplete="off" spellCheck={false} value={rotationCredential} onChange={(event) => setRotationCredential(event.target.value)} required /></label>{rotationError && <div className="finding finding--danger" role="alert">{rotationError}</div>}<div className="row-actions"><button type="button" className="button button--secondary" onClick={() => { setRotatingID(""); setRotationCredential(""); }}>Cancel</button><button className="button button--primary" disabled={props.busy}>Submit rotation</button></div></form>}
        {removingAccount && <div className="rotation-panel" role="group" aria-label={`Remove ${removingAccount.displayName}`}><div><strong>Remove {removingAccount.displayName}?</strong><span>Removal proceeds only when no open position, reservation, or command blocks the account.</span></div><div className="row-actions"><button type="button" className="button button--secondary" onClick={() => setRemovingID("")}>Cancel</button><button type="button" className="button button--danger" disabled={props.busy} onClick={async () => { await props.onAction(removingAccount.accountId, "remove"); setRemovingID(""); }}>Confirm removal</button></div></div>}
      </div>
      <form className="surface account-form" onSubmit={create}>
        <div className="section-heading"><div><h2>Add account</h2><span>Secret material is never returned</span></div><KeyRound /></div>
        <label>Venue<select value={venueKey} onChange={(event) => setVenueKey(event.target.value)} required>{available.map((venue) => <option key={`${venue.venue}:${venue.environment}`} value={`${venue.venue}:${venue.environment}`}>{venue.venue} · {venue.environment}</option>)}</select></label>
        <label>Account name<input value={displayName} onChange={(event) => setDisplayName(event.target.value)} required /></label>
        <fieldset><legend>Products</legend><label className="check-control"><input type="checkbox" checked={products.includes("funding")} onChange={() => toggleProduct("funding")} /><span>Funding</span></label><label className="check-control"><input type="checkbox" checked={products.includes("manual")} onChange={() => toggleProduct("manual")} /><span>Manual</span></label></fieldset>
        <label>Credential JSON<textarea rows={6} autoComplete="off" spellCheck={false} value={credential} onChange={(event) => setCredential(event.target.value)} required /></label>
        {formError && <div className="finding finding--danger" role="alert">{formError}</div>}
        <button className="button button--primary button--wide" disabled={props.busy || available.length === 0 || products.length === 0}><Plus />Add account</button>
      </form>
    </section>
    <section className="surface surface--table"><div className="section-heading"><div><h2>Capability discovery</h2><span>{props.capabilities.length} reported</span></div></div><div className="table-scroll"><table><thead><tr><th>Account</th><th>Venue</th><th>Product</th><th>Operation</th><th>Support</th></tr></thead><tbody>{props.capabilities.map((item) => <tr key={`${item.accountId}:${item.rowId}:${item.operation}`}><td><code>{item.accountId}</code></td><td>{item.venue}</td><td>{item.product}{item.settlement && <small>{item.settlement}</small>}</td><td>{item.operation}</td><td><StatusBadge state={item.supported ? (item.writeReady ? "ready" : "read_only") : "denied"} label={item.supported ? (item.writeReady ? "supported" : "read only") : "unsupported"} /></td></tr>)}</tbody></table></div>{props.capabilities.length === 0 && <div className="empty-row">Capability projection is not available yet.</div>}</section>
  </div>;
}
