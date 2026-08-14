import { KeyRound, Plus, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";

import { describeError } from "@/api/errors";
import type {
  Account,
  AccountCapability,
  AccountRoute,
  AccountRuntime,
  AccountStatus,
  PendingRuntimeSnapshot,
  RuntimeSnapshot,
  Venue,
} from "@/api/types";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Panel } from "@/components/ui/panel";
import { StateBadge, Tag, type StateTone } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import {
  buildCredentialBundle,
  CredentialFields,
  missingCredentialFields,
  type CredentialValues,
} from "@/features/settings/credential-form";
import { useAccounts } from "@/features/market/queries";
import {
  isRevisionConflict,
  useAccountRuntimes,
  useCreateAccount,
  useDisableAccount,
  useDrainAccount,
  useRemoveAccount,
  useRotateCredential,
  useValidateAccount,
  useVenues,
} from "@/features/settings/queries";
import { formatTimestamp } from "@/lib/format";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * Settings: the accounts, and what may be done to them.
 *
 * Two things shape this page.
 *
 * First, the lifecycle is a state machine and the operator is acting on a
 * revision, not on an object. Every mutation carries the `desiredStateRevision`
 * it was launched from, so a stale tab gets a 409 instead of silently undoing
 * somebody else's change. That is why the actions live next to the revision
 * they will send.
 *
 * Second, the gateway's record and trading's runtime are different facts and
 * both are shown. The record says what was asked for; the runtime says what has
 * been applied, and during a rotation it carries an active and a pending
 * generation at the same time. Collapsing the two into one "healthy" light
 * would hide exactly the state an operator opens this page to inspect.
 *
 * Forms expand inline rather than opening dialogs: no dialog component exists
 * here, and inline keeps the account being modified visible while it is edited.
 * ------------------------------------------------------------------------- */

const STATUS_TONE: Record<AccountStatus, StateTone> = {
  creating: "pending",
  active: "ok",
  rotating: "pending",
  draining: "warn",
  drained: "neutral",
  disabled: "neutral",
  removing: "warn",
  removed: "neutral",
};

/** Nothing may be asked of an account the gateway is already tearing down. */
const isTerminal = (status: AccountStatus): boolean => status === "removing" || status === "removed";

export function SettingsPage() {
  const accountsQuery = useAccounts();
  const venuesQuery = useVenues();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const list = accountsQuery.data ?? [];
  const venues = venuesQuery.data?.items ?? [];
  const selected = list.find((account) => account.accountId === selectedId) ?? null;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-bar shrink-0 items-center gap-3 border-b border-line px-3">
        <h1 className="text-title font-medium text-text">Accounts</h1>
        <Tag>{list.length}</Tag>
        <Button
          variant={creating ? "outline" : "primary"}
          size="sm"
          className="ml-auto"
          onClick={() => {
            setCreating((current) => !current);
            setSelectedId(null);
          }}
        >
          {creating ? "Cancel" : <><Plus className="size-3.5" aria-hidden="true" /> Add account</>}
        </Button>
      </header>

      <div className="flex min-h-0 flex-1">
        <nav aria-label="Accounts" className="w-72 shrink-0 overflow-y-auto border-r border-line">
          {list.map((account) => (
            <button
              key={account.accountId}
              type="button"
              onClick={() => {
                setSelectedId(account.accountId);
                setCreating(false);
              }}
              className={cn(
                "flex w-full flex-col gap-1 border-b border-line px-3 py-2 text-left",
                "hover:bg-raised",
                account.accountId === selectedId && !creating ? "bg-raised" : "",
              )}
            >
              <span className="flex items-center gap-2">
                <span className="min-w-0 truncate text-base text-text">{account.displayName}</span>
                <StateBadge
                  tone={STATUS_TONE[account.status]}
                  label={account.status}
                  className="ml-auto text-meta text-muted"
                />
              </span>
              <span className="truncate text-meta text-dim">
                {account.venue} · {account.nativeEnvironment} · {account.mode}
              </span>
            </button>
          ))}
          {list.length === 0 && !accountsQuery.isPending ? (
            <p className="p-3 text-meta text-dim">No accounts yet.</p>
          ) : null}
        </nav>

        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-3">
          {creating ? (
            <CreateAccount
              venues={venues}
              onCreated={(account) => {
                setCreating(false);
                setSelectedId(account.accountId);
              }}
            />
          ) : selected ? (
            <AccountDetail
              key={selected.accountId}
              account={selected}
              venue={venues.find((venue) => venue.venue === selected.venue) ?? null}
            />
          ) : (
            <EmptyState
              icon={KeyRound}
              title="No account selected"
              detail="Select an account to inspect its desired state, runtime and capabilities."
            />
          )}
        </div>
      </div>
    </div>
  );
}

/* --- Detail --------------------------------------------------------------- */

function AccountDetail({ account, venue }: { account: Account; venue: Venue | null }) {
  const toast = useToast();
  const runtimes = useAccountRuntimes(account.accountId);
  const [rotating, setRotating] = useState(false);
  const [confirmingRemove, setConfirmingRemove] = useState(false);

  const validate = useValidateAccount();
  const disable = useDisableAccount();
  const drain = useDrainAccount();
  const remove = useRemoveAccount();

  const busy = validate.isPending || disable.isPending || drain.isPending || remove.isPending;
  const terminal = isTerminal(account.status);

  const runtime = runtimes.data?.find((entry) => entry.accountId === account.accountId) ?? null;

  const announce = (title: string) => ({
    onSuccess: () => toast.push({ tone: "ok", title, detail: account.displayName }),
    onError: (error: unknown) =>
      toast.push({
        tone: "danger" as const,
        title: isRevisionConflict(error) ? "Account had already changed" : title.replace("requested", "failed"),
        detail: isRevisionConflict(error)
          ? "Someone else modified this account. It has been reloaded — check the state and try again."
          : describeError(error),
      }),
  });

  return (
    <div className="flex flex-col gap-3">
      <Panel
        title={account.displayName}
        meta={<StateBadge tone={STATUS_TONE[account.status]} label={account.status} />}
      >
        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-meta md:grid-cols-4">
          <Fact label="Venue" value={account.venue} />
          <Fact label="Environment" value={account.nativeEnvironment} />
          <Fact label="Mode" value={account.mode} />
          <Fact label="Products" value={account.productPolicy.join(", ")} />
          <Fact label="Desired revision" value={String(account.desiredStateRevision)} numeric />
          <Fact label="Runtime generation" value={String(account.runtimeGeneration)} numeric />
          <Fact label="Credential version" value={String(account.credentialVersion)} numeric />
          <Fact
            label="Last validated"
            value={account.lastValidatedAt === null ? "never" : formatTimestamp(account.lastValidatedAt)}
          />
        </dl>

        <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-line pt-3">
          <Button
            size="sm"
            disabled={busy || terminal}
            onClick={() => validate.mutate(account, announce("Validation requested"))}
          >
            <ShieldCheck className="size-3.5" aria-hidden="true" /> Validate
          </Button>
          <Button
            size="sm"
            disabled={busy || terminal || !venue}
            onClick={() => setRotating((current) => !current)}
          >
            {rotating ? "Cancel rotation" : "Rotate credential"}
          </Button>
          <Button
            size="sm"
            disabled={busy || terminal || account.status === "draining" || account.status === "drained"}
            onClick={() => drain.mutate(account, announce("Drain requested"))}
          >
            Drain
          </Button>
          <Button
            size="sm"
            disabled={busy || terminal || account.status === "disabled"}
            onClick={() => disable.mutate(account, announce("Disable requested"))}
          >
            Disable
          </Button>

          <span className="ml-auto flex items-center gap-2">
            {confirmingRemove ? (
              <>
                <span className="text-meta text-muted">Remove {account.displayName}?</span>
                <Button size="sm" variant="ghost" onClick={() => setConfirmingRemove(false)}>
                  Keep
                </Button>
                <Button
                  size="sm"
                  variant="danger"
                  disabled={busy}
                  onClick={() => {
                    setConfirmingRemove(false);
                    remove.mutate(account, announce("Removal requested"));
                  }}
                >
                  Remove for good
                </Button>
              </>
            ) : (
              <Button
                size="sm"
                variant="danger"
                disabled={busy || account.status === "removed"}
                onClick={() => setConfirmingRemove(true)}
              >
                Remove
              </Button>
            )}
          </span>
        </div>

        {/* Removal is accepted, not done: the credential is destroyed only once
            the runtime confirms the tombstone, which is why the account stays
            visible in `removing` rather than disappearing on click. */}
        {account.status === "removing" ? (
          <p className="mt-2 text-meta text-warn">
            Removal accepted. The credential is destroyed once the runtime confirms the tombstone.
          </p>
        ) : null}
      </Panel>

      {rotating && venue ? (
        <RotateCredential account={account} venue={venue} onDone={() => setRotating(false)} />
      ) : null}

      <Panel title="Routes" meta={`${account.routes?.length ?? 0} lanes`}>
        {account.routes && account.routes.length > 0 ? (
          <ul className="flex flex-col">
            {account.routes.map((route) => (
              <RouteRow key={route.routeId} route={route} />
            ))}
          </ul>
        ) : (
          <p className="text-meta text-dim">No routes on this account.</p>
        )}
      </Panel>

      <RuntimePanel runtime={runtime} loading={runtimes.isPending} />
    </div>
  );
}

function Fact({ label, value, numeric = false }: { label: string; value: string; numeric?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="th">{label}</dt>
      <dd className="truncate text-base text-muted" {...(numeric ? { "data-numeric": "" } : {})}>
        {value}
      </dd>
    </div>
  );
}

function RouteRow({ route }: { route: AccountRoute }) {
  return (
    <li className="flex h-row items-center gap-3 text-base">
      <span className="min-w-0 flex-1 truncate">{route.routeId}</span>
      <span className="shrink-0 text-meta text-muted">{route.product}</span>
      <span className="shrink-0 text-meta text-dim">{route.settlementAsset ?? "—"}</span>
      <span className="min-w-0 flex-1 truncate text-right text-meta text-dim">{route.routeProfileId}</span>
      <StateBadge
        tone={route.requestedState === "active" ? "ok" : "neutral"}
        label={route.requestedState}
        className="w-20 shrink-0 justify-end text-meta text-muted"
      />
    </li>
  );
}

/* --- Runtime -------------------------------------------------------------- */

function RuntimePanel({ runtime, loading }: { runtime: AccountRuntime | null; loading: boolean }) {
  if (!runtime) {
    return (
      <Panel title="Runtime">
        <p className="text-meta text-dim">
          {loading ? "Reading trading's runtime…" : "Trading has no runtime for this account yet."}
        </p>
      </Panel>
    );
  }

  return (
    <Panel title="Runtime" meta={`observed ${formatTimestamp(runtime.observedAt)}`}>
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
        <Readiness label="Read" ready={runtime.readiness.ready} reason={runtime.readiness.reason} />
        <Readiness label="Write" ready={runtime.writeReadiness.ready} reason={runtime.writeReadiness.reason} />
        <span className="text-meta text-dim" data-numeric="">
          applied rev {runtime.lastAppliedDesiredStateRevision} · gen {runtime.lastAppliedRuntimeGeneration}
        </span>
      </div>

      {runtime.blockedReason !== null ? (
        <p className="mt-2 text-meta text-warn">Blocked: {runtime.blockedReason}</p>
      ) : null}

      <div className="mt-3 flex flex-col gap-3 border-t border-line pt-3">
        {runtime.active ? <Generation title="Active" snapshot={runtime.active} tone="ok" /> : null}
        {runtime.pending ? <Generation title="Pending" snapshot={runtime.pending} tone="pending" /> : null}
        {runtime.retiring.map((snapshot) => (
          <Generation
            key={`retiring-${snapshot.runtimeGeneration}`}
            title="Retiring"
            snapshot={snapshot}
            tone="neutral"
          />
        ))}
        {!runtime.active && !runtime.pending && runtime.retiring.length === 0 ? (
          <p className="text-meta text-dim">No generation is loaded.</p>
        ) : null}
      </div>

      {runtime.capabilities.length > 0 ? (
        <div className="mt-3 flex flex-col gap-2 border-t border-line pt-3">
          <div className="th">Capabilities</div>
          {runtime.capabilities.map((capability) => (
            <Capability key={capability.rowId} capability={capability} />
          ))}
        </div>
      ) : null}
    </Panel>
  );
}

function Readiness({ label, ready, reason }: { label: string; ready: boolean; reason?: string }) {
  return (
    <StateBadge
      tone={ready ? "ok" : "warn"}
      label={`${label}: ${ready ? "ready" : "not ready"}`}
      detail={reason}
      className="text-base"
    />
  );
}

/**
 * One credential generation. During a rotation there are two or three of these
 * at once and the titles are the point — which one is serving traffic, which is
 * proving itself, which is on its way out.
 */
function Generation({
  title,
  snapshot,
  tone,
}: {
  title: string;
  snapshot: RuntimeSnapshot | PendingRuntimeSnapshot;
  tone: StateTone;
}) {
  const pending = "state" in snapshot ? snapshot : null;
  return (
    <div className="min-w-0">
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge tone={tone} label={title} className="text-base text-text" />
        <span className="text-meta text-dim" data-numeric="">
          rev {snapshot.desiredStateRevision} · gen {snapshot.runtimeGeneration} · cred{" "}
          {snapshot.credentialVersion}
        </span>
        <Tag>{snapshot.requestedState}</Tag>
        {pending ? <Tag>{pending.state}</Tag> : null}
      </div>
      {pending && pending.reason !== "" ? (
        <p className="mt-1 text-meta text-muted">{pending.reason}</p>
      ) : null}
      <ul className="mt-1 flex flex-col">
        {snapshot.routes.map((route) => (
          <li key={route.routeId} className="flex h-row items-center gap-3 text-meta">
            <span className="min-w-0 flex-1 truncate text-base">{route.routeId}</span>
            <span className="shrink-0 text-dim">{route.product}</span>
            <StateBadge
              tone={route.ready ? "ok" : "warn"}
              label={route.ready ? "ready" : "not ready"}
              detail={route.reason}
              className="w-24 shrink-0 justify-end text-muted"
            />
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The venue's answer to "what may this account do", not the product's wish. */
function Capability({ capability }: { capability: AccountCapability }) {
  const allowed = Object.entries(capability.operations)
    .filter(([, enabled]) => enabled)
    .map(([operation]) => operation);

  return (
    <div className="min-w-0">
      <div className="flex items-center gap-2 text-meta">
        <span className="text-base text-muted">
          {capability.product}
          {capability.settlement ? ` · ${capability.settlement}` : ""}
        </span>
        <StateBadge
          tone={capability.writeReady ? "ok" : "warn"}
          label={capability.writeReady ? "write ready" : "read only"}
          className="text-muted"
        />
      </div>
      <div className="mt-1 flex flex-wrap gap-1">
        {allowed.length === 0 ? (
          <span className="text-meta text-dim">No operation is permitted.</span>
        ) : (
          allowed.map((operation) => <Tag key={operation}>{operation}</Tag>)
        )}
      </div>
    </div>
  );
}

/* --- Credential writes ---------------------------------------------------- */

function RotateCredential({
  account,
  venue,
  onDone,
}: {
  account: Account;
  venue: Venue;
  onDone: () => void;
}) {
  const toast = useToast();
  const rotate = useRotateCredential();
  const [values, setValues] = useState<CredentialValues>({});
  const [error, setError] = useState<string | null>(null);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const missing = missingCredentialFields(venue, values);
    if (missing.length > 0) {
      setError(`Still needed: ${missing.join(", ")}.`);
      return;
    }
    setError(null);
    rotate.mutate(
      { account, credentialBundle: buildCredentialBundle(venue, values) },
      {
        // Cleared either way. A submitted credential does not stay on screen,
        // and a rejected one is re-entered rather than corrected in place.
        onSettled: () => setValues({}),
        onSuccess: () => {
          toast.push({
            tone: "ok",
            title: "Rotation started",
            detail: "The account keeps serving on the active credential until the new one is proven.",
          });
          onDone();
        },
        onError: (rotateError) =>
          toast.push({
            tone: "danger",
            title: isRevisionConflict(rotateError) ? "Account had already changed" : "Rotation refused",
            detail: isRevisionConflict(rotateError)
              ? "Someone else modified this account. It has been reloaded — re-enter the credential and try again."
              : describeError(rotateError),
          }),
      },
    );
  };

  return (
    <Panel title="Rotate credential" meta={`${venue.credentialSchema.authKind} · v${venue.credentialSchema.schemaVersion}`}>
      <form onSubmit={submit} className="flex max-w-lg flex-col gap-3">
        <p className="text-meta text-dim">
          The new credential is proven before it takes over. Until it does, the account keeps trading on
          the current one and both generations show above.
        </p>
        <CredentialFields
          venue={venue}
          values={values}
          onChange={(key, value) => setValues((current) => ({ ...current, [key]: value }))}
          disabled={rotate.isPending}
        />
        {error ? <p className="text-meta text-danger">{error}</p> : null}
        <div className="flex items-center gap-2">
          <Button type="submit" variant="primary" size="sm" disabled={rotate.isPending}>
            {rotate.isPending ? "Rotating…" : "Rotate"}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={onDone}>
            Cancel
          </Button>
        </div>
      </form>
    </Panel>
  );
}

/** The gateway accepts exactly this and nothing else, so it is not a control. */
const PRODUCT_POLICY = ["manual"];

function CreateAccount({ venues, onCreated }: { venues: Venue[]; onCreated: (account: Account) => void }) {
  const toast = useToast();
  const create = useCreateAccount();
  const [venueName, setVenueName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [routeIds, setRouteIds] = useState<string[]>([]);
  const [values, setValues] = useState<CredentialValues>({});
  const [error, setError] = useState<string | null>(null);

  const venue = venues.find((candidate) => candidate.venue === venueName) ?? null;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!venue) {
      setError("Choose a venue.");
      return;
    }
    if (displayName.trim() === "") {
      setError("A display name is required.");
      return;
    }
    const routes: AccountRoute[] = venue.routeProfiles
      .filter((profile) => routeIds.includes(profile.routeId))
      .map((profile) => ({
        routeId: profile.routeId,
        product: profile.product,
        ...(profile.settlementAsset ? { settlementAsset: profile.settlementAsset } : {}),
        routeProfileId: profile.routeProfileId,
        requestedState: "active" as const,
      }));
    if (routes.length === 0) {
      setError("Select at least one route — the routes decide the account's mode and environment.");
      return;
    }
    const missing = missingCredentialFields(venue, values);
    if (missing.length > 0) {
      setError(`Still needed: ${missing.join(", ")}.`);
      return;
    }
    setError(null);

    create.mutate(
      {
        venue: venue.venue,
        displayName: displayName.trim(),
        productPolicy: PRODUCT_POLICY,
        routes,
        credentialBundle: buildCredentialBundle(venue, values),
      },
      {
        onSettled: () => setValues({}),
        onSuccess: (account) => {
          toast.push({ tone: "ok", title: "Account created", detail: account.accountId });
          onCreated(account);
        },
        onError: (createError) =>
          toast.push({ tone: "danger", title: "Account refused", detail: describeError(createError) }),
      },
    );
  };

  return (
    <Panel title="New account">
      <form onSubmit={submit} className="flex max-w-lg flex-col gap-3">
        <Field
          label="Venue"
          required
          hint="The route profiles you pick below decide the mode and native environment."
        >
          {(field) => (
            <select
              {...field}
              className={inputClass}
              value={venueName}
              onChange={(event) => {
                setVenueName(event.target.value);
                // Routes and credential fields belong to the venue that was
                // showing when they were entered. Keeping them would submit one
                // venue's key under another's schema.
                setRouteIds([]);
                setValues({});
                setError(null);
              }}
            >
              <option value="">Choose a venue…</option>
              {venues.map((candidate) => (
                <option key={candidate.venue} value={candidate.venue}>
                  {candidate.venue} · {candidate.nativeEnvironment}
                  {candidate.ready ? "" : " (not ready)"}
                </option>
              ))}
            </select>
          )}
        </Field>

        <Field label="Display name" required>
          {(field) => (
            <Input
              {...field}
              value={displayName}
              autoComplete="off"
              placeholder="Binance testnet — manual"
              onChange={(event) => setDisplayName(event.target.value)}
            />
          )}
        </Field>

        {venue ? (
          <>
            <fieldset className="flex flex-col gap-1.5">
              <legend className="th">
                Routes <span className="text-danger" aria-hidden="true">*</span>
              </legend>
              {venue.routeProfiles.map((profile) => (
                <label key={profile.routeId} className="flex h-row items-center gap-2 text-base">
                  <input
                    type="checkbox"
                    className="size-3.5 shrink-0 accent-[var(--bt-accent)]"
                    checked={routeIds.includes(profile.routeId)}
                    onChange={(event) =>
                      setRouteIds((current) =>
                        event.target.checked
                          ? [...current, profile.routeId]
                          : current.filter((id) => id !== profile.routeId),
                      )
                    }
                  />
                  <span className="min-w-0 truncate">{profile.label}</span>
                  <span className="ml-auto shrink-0 text-meta text-dim">
                    {profile.product}
                    {profile.settlementAsset ? ` · ${profile.settlementAsset}` : ""}
                  </span>
                </label>
              ))}
              {venue.routeProfiles.length === 0 ? (
                <p className="text-meta text-dim">This venue publishes no route profiles.</p>
              ) : null}
            </fieldset>

            <CredentialFields
              venue={venue}
              values={values}
              onChange={(key, value) => setValues((current) => ({ ...current, [key]: value }))}
              disabled={create.isPending}
            />
          </>
        ) : null}

        {error ? <p className="text-meta text-danger">{error}</p> : null}

        <div className="flex items-center gap-2">
          <Button type="submit" variant="primary" size="sm" disabled={create.isPending || !venue}>
            {create.isPending ? "Creating…" : "Create account"}
          </Button>
          <span className="text-meta text-dim">Trades manually. Other products are not accepted here.</span>
        </div>
      </form>
    </Panel>
  );
}
