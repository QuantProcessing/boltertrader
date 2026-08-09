import { Ban, CircleCheck, CircleStop, KeyRound, Pencil, Plus, RefreshCw, RotateCw, Trash2 } from "lucide-react";
import { Fragment, type FormEvent, useMemo, useState } from "react";

import type { Account, AccountRoute, Capability, CredentialSchema, Mode, RouteRuntimeView, RuntimeSnapshotView, RuntimeView, Venue } from "../types";
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

type RouteDiagnostic = {
  phase: "Active" | "Pending" | "Retiring";
  lifecycle: string;
  route: RouteRuntimeView;
};

const fallbackSchema: CredentialSchema = {
  schemaVersion: 1,
  authKind: "api_key_secret",
  fields: [{ key: "apiKey", label: "API key" }, { key: "apiSecret", label: "API secret" }],
};

const accountProducts = (account: Account) => account.productPolicy ?? account.productIds ?? [];
const accountEnvironment = (account: Account) => account.nativeEnvironment ?? account.venueEnvironment ?? "unavailable";

function credentialBundle(venue: Venue, values: Record<string, string>) {
  const schema = venue.credentialSchema ?? fallbackSchema;
  const metadata: Record<string, string> = {};
  const secrets: Record<string, string> = {};
  for (const field of schema.fields) (field.metadata ? metadata : secrets)[field.key] = values[field.key] ?? "";
  return { schemaVersion: schema.schemaVersion, venue: venue.venue, authKind: schema.authKind, metadata, secrets };
}

function diagnostics(runtime: RuntimeView | undefined): RouteDiagnostic[] {
  if (!runtime) return [];
  const rows: RouteDiagnostic[] = [];
  const add = (phase: RouteDiagnostic["phase"], snapshot: RuntimeSnapshotView | undefined, lifecycle?: string) => {
    if (!snapshot) return;
    for (const route of snapshot.routes) rows.push({ phase, lifecycle: lifecycle ?? snapshot.requestedState, route });
  };
  add("Active", runtime.active);
  add("Pending", runtime.pending, runtime.pending?.state);
  for (const snapshot of runtime.retiring ?? []) add("Retiring", snapshot);
  return rows;
}

function IconAction({ label, title, onClick, disabled, danger, children }: {
  label: string;
  title?: string;
  onClick: () => void;
  disabled?: boolean;
  danger?: boolean;
  children: React.ReactNode;
}) {
  return <button type="button" className={`icon-button${danger ? " icon-button--danger" : ""}`} aria-label={label} title={title ?? label} onClick={onClick} disabled={disabled}>{children}</button>;
}

export function AccountsRoute(props: AccountsProps) {
  const available = useMemo(() => props.venues.filter((venue) => venue.ready && (props.mode === "live" ? venue.environment === "live" : venue.environment !== "live")), [props.mode, props.venues]);
  const [venueKey, setVenueKey] = useState(available[0] ? `${available[0].venue}:${available[0].environment}` : "");
  const selectedVenue = available.find((venue) => `${venue.venue}:${venue.environment}` === venueKey) ?? available[0];
  const schema = selectedVenue?.credentialSchema ?? fallbackSchema;
  const profiles = selectedVenue?.routeProfiles ?? [];
  const [displayName, setDisplayName] = useState("");
  const [credentials, setCredentials] = useState<Record<string, string>>({});
  const [products, setProducts] = useState(["manual"]);
  const [selectedRoutes, setSelectedRoutes] = useState<string[]>(profiles.slice(-1).map((profile) => profile.routeId));
  const [formError, setFormError] = useState("");
  const [rotatingID, setRotatingID] = useState("");
  const [rotationCredentials, setRotationCredentials] = useState<Record<string, string>>({});
  const [rotationError, setRotationError] = useState("");
  const [editingID, setEditingID] = useState("");
  const [editDisplayName, setEditDisplayName] = useState("");
  const [editProducts, setEditProducts] = useState<string[]>([]);
  const [editError, setEditError] = useState("");
  const [removingID, setRemovingID] = useState("");

  const clearCredentials = () => setCredentials({});
  const changeVenue = (key: string) => {
    const venue = available.find((item) => `${item.venue}:${item.environment}` === key);
    setVenueKey(key);
    clearCredentials();
    setSelectedRoutes((venue?.routeProfiles ?? []).slice(-1).map((profile) => profile.routeId));
  };
  const create = async (event: FormEvent) => {
    event.preventDefault();
    setFormError("");
    if (!selectedVenue) return;
    const routes: AccountRoute[] = profiles.filter((profile) => selectedRoutes.includes(profile.routeId)).map(({ label: _label, ...profile }) => ({ ...profile, requestedState: "active" }));
    try {
      const productPolicy = ["funding", "manual"].filter((product) => products.includes(product));
      await props.onCreate({ venue: selectedVenue.venue, displayName: displayName.trim(), productPolicy, routes, credentialBundle: credentialBundle(selectedVenue, credentials) });
      setDisplayName("");
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "Account creation failed.");
    } finally {
      clearCredentials();
    }
  };

  const toggle = (value: string, update: React.Dispatch<React.SetStateAction<string[]>>) => update((current) => current.includes(value) ? current.filter((item) => item !== value) : [...current, value]);
  const rotatingAccount = props.accounts.find((account) => account.accountId === rotatingID);
  const rotatingVenue = available.find((venue) => venue.venue === rotatingAccount?.venue) ?? props.venues.find((venue) => venue.venue === rotatingAccount?.venue);
  const rotationSchema = rotatingVenue?.credentialSchema ?? fallbackSchema;
  const editingAccount = props.accounts.find((account) => account.accountId === editingID);
  const removingAccount = props.accounts.find((account) => account.accountId === removingID);
  const edit = (account: Account) => {
    setEditingID(account.accountId);
    setEditDisplayName(account.displayName);
    setEditProducts([...accountProducts(account)]);
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
    await props.onAction(editingID, "update", { displayName: editDisplayName.trim(), productPolicy: editProducts });
    setEditingID("");
  };
  const rotate = async (event: FormEvent) => {
    event.preventDefault();
    setRotationError("");
    if (!rotatingVenue) return;
    try {
      await props.onAction(rotatingID, "rotate", { credentialBundle: credentialBundle(rotatingVenue, rotationCredentials) });
      setRotatingID("");
    } catch (error) {
      setRotationError(error instanceof Error ? error.message : "Credential rotation failed.");
    } finally {
      setRotationCredentials({});
    }
  };

  return <div className="route-stack">
    <header className="page-heading"><div><p className="eyebrow">Platform / Security</p><h1>Accounts &amp; Security</h1></div><button className="button button--secondary" onClick={props.onRefresh}><RefreshCw />Refresh</button></header>
    <section className="accounts-layout">
      <div className="surface surface--table">
        <div className="section-heading"><div><h2>Trading accounts</h2><span>{props.accounts.length} configured</span></div></div>
        <div className="table-scroll"><table className="account-table"><thead><tr><th>Account</th><th>Venue</th><th>Policy</th><th>State</th><th>Actions</th></tr></thead><tbody>{props.accounts.map((account) => <Fragment key={account.accountId}>
          <tr><td><strong>{account.displayName}</strong><small>{account.accountId}</small></td><td>{account.venue}<small>{accountEnvironment(account)}</small></td><td>{accountProducts(account).join(", ")}</td><td><StatusBadge state={account.status} /></td><td><div className="row-actions">
            <IconAction label={`Edit ${account.displayName}`} onClick={() => edit(account)} disabled={props.busy}><Pencil /></IconAction>
            <IconAction label={`Retry validation for ${account.displayName}`} onClick={() => void props.onAction(account.accountId, "validate")} disabled={props.busy}><CircleCheck /></IconAction>
            <IconAction label={`Rotate ${account.displayName} credentials`} onClick={() => { setRotatingID(account.accountId); setRotationCredentials({}); setRotationError(""); setEditingID(""); setRemovingID(""); }} disabled={props.busy}><RotateCw /></IconAction>
            <IconAction label={`Drain ${account.displayName}`} onClick={() => void props.onAction(account.accountId, "drain")} disabled={props.busy}><CircleStop /></IconAction>
            <IconAction label={`Disable ${account.displayName}`} onClick={() => void props.onAction(account.accountId, "disable")} disabled={props.busy}><Ban /></IconAction>
            <IconAction label={`Remove ${account.displayName}`} onClick={() => { setRemovingID(account.accountId); setEditingID(""); setRotatingID(""); }} disabled={props.busy} danger><Trash2 /></IconAction>
          </div></td></tr>
          {diagnostics(props.runtimes[account.accountId]?.[0]).map((item, index) => <tr className="route-diagnostic" key={`${item.phase}:${item.route.routeId}:${index}`} aria-label={`${item.phase} ${item.route.routeId}`}><td><span className="route-phase">{item.phase}</span><small>{item.route.routeId}</small></td><td>{item.route.product}<small>{item.route.settlementAsset ?? "native"}</small></td><td><code>{item.route.routeProfileId}</code></td><td><StatusBadge state={item.lifecycle} /><small>{item.route.ready ? "ready" : "not ready"}</small></td><td>{item.route.reason ?? (item.route.ready ? "Ready" : "No reason reported")}</td></tr>)}
        </Fragment>)}</tbody></table></div>
        {props.accounts.length === 0 && <div className="empty-state"><CircleCheck /><strong>No accounts configured</strong><span>Platform is ready for account provisioning.</span></div>}
        {editingAccount && <form className="rotation-panel" onSubmit={saveEdit}><div><strong>Edit {editingAccount.displayName}</strong></div><label>{`Account name for ${editingAccount.displayName}`}<input value={editDisplayName} onChange={(event) => setEditDisplayName(event.target.value)} required /></label><fieldset><legend>Product policy</legend>{["funding", "manual"].map((product) => <label className="check-control" key={product}><input aria-label={`${product[0].toUpperCase()}${product.slice(1)} product for ${editingAccount.displayName}`} type="checkbox" checked={editProducts.includes(product)} onChange={() => toggle(product, setEditProducts)} /><span>{product}</span></label>)}</fieldset>{editError && <div className="finding finding--danger" role="alert">{editError}</div>}<div className="row-actions"><button type="button" className="button button--secondary" onClick={() => setEditingID("")}>Cancel</button><button className="button button--primary" disabled={props.busy}>Save account</button></div></form>}
        {rotatingAccount && rotatingVenue && <form className="rotation-panel" onSubmit={rotate}><div><strong>Rotate {rotatingAccount.displayName}</strong><span>Replacement fields are write-only.</span></div>{rotationSchema.fields.map((field) => <label key={field.key}>{`Replacement ${field.label} for ${rotatingAccount.displayName}`}<input type="password" autoComplete="new-password" value={rotationCredentials[field.key] ?? ""} onChange={(event) => setRotationCredentials((current) => ({ ...current, [field.key]: event.target.value }))} required /></label>)}{rotationError && <div className="finding finding--danger" role="alert">{rotationError}</div>}<div className="row-actions"><button type="button" className="button button--secondary" onClick={() => { setRotatingID(""); setRotationCredentials({}); }}>Cancel</button><button className="button button--primary" disabled={props.busy}>Submit rotation</button></div></form>}
        {removingAccount && <div className="rotation-panel" role="group" aria-label={`Remove ${removingAccount.displayName}`}><div><strong>Remove {removingAccount.displayName}?</strong><span>Open positions, reservations, or commands may block removal.</span></div><div className="row-actions"><button type="button" className="button button--secondary" onClick={() => setRemovingID("")}>Cancel</button><button type="button" className="button button--danger" disabled={props.busy} onClick={async () => { await props.onAction(removingAccount.accountId, "remove"); setRemovingID(""); }}>Confirm removal</button></div></div>}
      </div>
      <form className="surface account-form" onSubmit={create}>
        <div className="section-heading"><div><h2>Add account</h2><span>Credential fields are write-only</span></div><KeyRound /></div>
        <label>Venue<select value={venueKey} onChange={(event) => changeVenue(event.target.value)} required>{available.map((venue) => <option key={`${venue.venue}:${venue.environment}`} value={`${venue.venue}:${venue.environment}`}>{venue.venue}</option>)}</select></label>
        {selectedVenue && <dl className="profile-metadata"><dt>Native environment</dt><dd>{selectedVenue.nativeEnvironment ?? selectedVenue.environment}</dd></dl>}
        <label>Account name<input value={displayName} onChange={(event) => setDisplayName(event.target.value)} required /></label>
        <fieldset><legend>Product policy</legend>{["funding", "manual"].map((product) => <label className="check-control" key={product}><input aria-label={`${product[0].toUpperCase()}${product.slice(1)} product`} type="checkbox" checked={products.includes(product)} onChange={() => toggle(product, setProducts)} /><span>{product}</span></label>)}</fieldset>
        <fieldset className="route-options"><legend>Routes</legend>{profiles.map((profile) => <label className="check-control" key={profile.routeId}><input aria-label={`${profile.label} route`} type="checkbox" checked={selectedRoutes.includes(profile.routeId)} onChange={() => toggle(profile.routeId, setSelectedRoutes)} /><span>{profile.label}</span></label>)}</fieldset>
        {schema.fields.map((field) => <label key={field.key}>{field.label}<input type="password" autoComplete="new-password" value={credentials[field.key] ?? ""} onChange={(event) => setCredentials((current) => ({ ...current, [field.key]: event.target.value }))} required /></label>)}
        {formError && <div className="finding finding--danger" role="alert">{formError}</div>}
        <button className="button button--primary button--wide" disabled={props.busy || !selectedVenue || products.length === 0 || selectedRoutes.length === 0}><Plus />Add account</button>
      </form>
    </section>
    <section className="surface surface--table"><div className="section-heading"><div><h2>Capability discovery</h2><span>{props.capabilities.length} reported</span></div></div><div className="table-scroll"><table><thead><tr><th>Account</th><th>Venue</th><th>Product</th><th>Operation</th><th>Support</th></tr></thead><tbody>{props.capabilities.map((item) => <tr key={`${item.accountId}:${item.rowId}:${item.operation}`}><td><code>{item.accountId}</code></td><td>{item.venue}</td><td>{item.product}{item.settlement && <small>{item.settlement}</small>}</td><td>{item.operation}</td><td><StatusBadge state={item.supported ? (item.writeReady ? "ready" : "read_only") : "denied"} label={item.supported ? (item.writeReady ? "supported" : "read only") : "unsupported"} /></td></tr>)}</tbody></table></div>{props.capabilities.length === 0 && <div className="empty-row">Capability projection is not available yet.</div>}</section>
  </div>;
}
