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
import { useT, type Key } from "@/lib/i18n";
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
  const t = useT();
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
        <h1 className="text-title font-medium text-text">{t("settings.accounts")}</h1>
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
          {creating ? (
            t("settings.cancel")
          ) : (
            <>
              <Plus className="size-3.5" aria-hidden="true" /> {t("settings.addAccount")}
            </>
          )}
        </Button>
      </header>

      <div className="flex min-h-0 flex-1">
        <nav aria-label={t("settings.accounts")} className="w-72 shrink-0 overflow-y-auto border-r border-line">
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
                {account.venue} · {account.nativeEnvironment}
              </span>
            </button>
          ))}
          {list.length === 0 && !accountsQuery.isPending ? (
            <p className="p-3 text-meta text-dim">{t("settings.noAccounts")}</p>
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
              title={t("settings.empty.title")}
              detail={t("settings.empty.detail")}
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
  const t = useT();
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

  // Two keys rather than one edited at runtime. The failure title used to be the
  // success title with "requested" swapped for "failed", which is a sentence-level
  // edit that only holds in English: any other language has no such substring, so
  // the replace would be a no-op and a failed mutation would announce success.
  const announce = (requested: Key, failed: Key) => ({
    onSuccess: () => toast.push({ tone: "ok", title: t(requested), detail: account.displayName }),
    onError: (error: unknown) =>
      toast.push({
        tone: "danger" as const,
        title: isRevisionConflict(error) ? t("settings.conflict") : t(failed),
        detail: isRevisionConflict(error) ? t("settings.conflict.detail") : describeError(error, t),
      }),
  });

  return (
    <div className="flex flex-col gap-3">
      <Panel
        title={account.displayName}
        meta={<StateBadge tone={STATUS_TONE[account.status]} label={account.status} />}
      >
        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-meta md:grid-cols-4">
          <Fact label={t("settings.venue")} value={account.venue} />
          <Fact label={t("settings.environment")} value={account.nativeEnvironment} />
          <Fact label={t("settings.products")} value={account.productPolicy.join(", ")} />
          <Fact label={t("settings.desiredRevision")} value={String(account.desiredStateRevision)} numeric />
          <Fact label={t("settings.runtimeGeneration")} value={String(account.runtimeGeneration)} numeric />
          <Fact label={t("settings.credentialVersion")} value={String(account.credentialVersion)} numeric />
          <Fact
            label={t("settings.lastValidated")}
            value={
              account.lastValidatedAt === null ? t("settings.never") : formatTimestamp(account.lastValidatedAt)
            }
          />
        </dl>

        <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-line pt-3">
          <Button
            size="sm"
            disabled={busy || terminal}
            onClick={() =>
              validate.mutate(account, announce("settings.validate.requested", "settings.validate.failed"))
            }
          >
            <ShieldCheck className="size-3.5" aria-hidden="true" /> {t("settings.validate")}
          </Button>
          <Button
            size="sm"
            disabled={busy || terminal || !venue}
            onClick={() => setRotating((current) => !current)}
          >
            {rotating ? t("settings.cancelRotation") : t("settings.rotateCredential")}
          </Button>
          <Button
            size="sm"
            disabled={busy || terminal || account.status === "draining" || account.status === "drained"}
            onClick={() => drain.mutate(account, announce("settings.drain.requested", "settings.drain.failed"))}
          >
            {t("settings.drain")}
          </Button>
          <Button
            size="sm"
            disabled={busy || terminal || account.status === "disabled"}
            onClick={() =>
              disable.mutate(account, announce("settings.disable.requested", "settings.disable.failed"))
            }
          >
            {t("settings.disable")}
          </Button>

          <span className="ml-auto flex items-center gap-2">
            {confirmingRemove ? (
              <>
                <span className="text-meta text-muted">
                  {t("settings.remove.confirm", { name: account.displayName })}
                </span>
                <Button size="sm" variant="ghost" onClick={() => setConfirmingRemove(false)}>
                  {t("settings.keep")}
                </Button>
                <Button
                  size="sm"
                  variant="danger"
                  disabled={busy}
                  onClick={() => {
                    setConfirmingRemove(false);
                    remove.mutate(account, announce("settings.remove.requested", "settings.remove.failed"));
                  }}
                >
                  {t("settings.removeForGood")}
                </Button>
              </>
            ) : (
              <Button
                size="sm"
                variant="danger"
                disabled={busy || account.status === "removed"}
                onClick={() => setConfirmingRemove(true)}
              >
                {t("settings.remove")}
              </Button>
            )}
          </span>
        </div>

        {/* Removal is accepted, not done: the credential is destroyed only once
            the runtime confirms the tombstone, which is why the account stays
            visible in `removing` rather than disappearing on click. */}
        {account.status === "removing" ? (
          <p className="mt-2 text-meta text-warn">{t("settings.remove.accepted")}</p>
        ) : null}
      </Panel>

      {rotating && venue ? (
        <RotateCredential account={account} venue={venue} onDone={() => setRotating(false)} />
      ) : null}

      <Panel title={t("settings.routes")} meta={t("settings.lanes", { count: account.routes?.length ?? 0 })}>
        {account.routes && account.routes.length > 0 ? (
          <ul className="flex flex-col">
            {account.routes.map((route) => (
              <RouteRow key={route.routeId} route={route} />
            ))}
          </ul>
        ) : (
          <p className="text-meta text-dim">{t("settings.noRoutes")}</p>
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
  const t = useT();

  if (!runtime) {
    return (
      <Panel title={t("settings.runtime")}>
        <p className="text-meta text-dim">
          {loading ? t("settings.runtime.loading") : t("settings.runtime.absent")}
        </p>
      </Panel>
    );
  }

  return (
    <Panel
      title={t("settings.runtime")}
      meta={t("settings.observed", { time: formatTimestamp(runtime.observedAt) })}
    >
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
        <Readiness
          label={t("settings.read")}
          ready={runtime.readiness.ready}
          reason={runtime.readiness.reason}
        />
        <Readiness
          label={t("settings.write")}
          ready={runtime.writeReadiness.ready}
          reason={runtime.writeReadiness.reason}
        />
        <span className="text-meta text-dim" data-numeric="">
          {t("settings.applied", {
            revision: runtime.lastAppliedDesiredStateRevision,
            generation: runtime.lastAppliedRuntimeGeneration,
          })}
        </span>
      </div>

      {runtime.blockedReason !== null ? (
        <p className="mt-2 text-meta text-warn">{t("settings.blocked", { reason: runtime.blockedReason })}</p>
      ) : null}

      <div className="mt-3 flex flex-col gap-3 border-t border-line pt-3">
        {runtime.active ? (
          <Generation title={t("settings.active")} snapshot={runtime.active} tone="ok" />
        ) : null}
        {runtime.pending ? (
          <Generation title={t("settings.pending")} snapshot={runtime.pending} tone="pending" />
        ) : null}
        {runtime.retiring.map((snapshot) => (
          <Generation
            key={`retiring-${snapshot.runtimeGeneration}`}
            title={t("settings.retiring")}
            snapshot={snapshot}
            tone="neutral"
          />
        ))}
        {!runtime.active && !runtime.pending && runtime.retiring.length === 0 ? (
          <p className="text-meta text-dim">{t("settings.noGeneration")}</p>
        ) : null}
      </div>

      {runtime.capabilities.length > 0 ? (
        <div className="mt-3 flex flex-col gap-2 border-t border-line pt-3">
          <div className="th">{t("settings.capabilities")}</div>
          {runtime.capabilities.map((capability) => (
            <Capability key={capability.rowId} capability={capability} />
          ))}
        </div>
      ) : null}
    </Panel>
  );
}

function Readiness({ label, ready, reason }: { label: string; ready: boolean; reason?: string }) {
  const t = useT();
  return (
    <StateBadge
      tone={ready ? "ok" : "warn"}
      // One interpolated key rather than `${label}: ${state}` assembled here:
      // the separator is a full-width colon in Chinese, and a half-width one
      // between two Chinese words reads as a typesetting mistake.
      label={t("settings.readiness", {
        label,
        state: ready ? t("settings.ready") : t("settings.notReady"),
      })}
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
  const t = useT();
  const pending = "state" in snapshot ? snapshot : null;
  return (
    <div className="min-w-0">
      <div className="flex flex-wrap items-center gap-2">
        {/* `title` arrives translated: the caller knows which of active,
            pending or retiring this is, and this component only lays it out. */}
        <StateBadge tone={tone} label={title} className="text-base text-text" />
        <span className="text-meta text-dim" data-numeric="">
          {t("settings.generation", {
            revision: snapshot.desiredStateRevision,
            generation: snapshot.runtimeGeneration,
            credential: snapshot.credentialVersion,
          })}
        </span>
        {/* `requestedState`, the pending `state` and `reason` are the runtime's
            own words and stay in them, same as the blotter's command states. */}
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
              label={route.ready ? t("settings.ready") : t("settings.notReady")}
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
  const t = useT();
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
          label={capability.writeReady ? t("settings.writeReady") : t("settings.capability.readOnly")}
          className="text-muted"
        />
      </div>
      <div className="mt-1 flex flex-wrap gap-1">
        {allowed.length === 0 ? (
          <span className="text-meta text-dim">{t("settings.noOperation")}</span>
        ) : (
          // The operation names are the venue's vocabulary — `place`, `cancel`,
          // `query` — and read the same in a support ticket as on screen.
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
  const t = useT();
  const [values, setValues] = useState<CredentialValues>({});
  const [error, setError] = useState<string | null>(null);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const missing = missingCredentialFields(venue, values);
    if (missing.length > 0) {
      // The field names come from the venue's credential schema, so the list
      // stays in the venue's spelling; only the sentence around it translates.
      setError(t("settings.stillNeeded", { fields: missing.join(", ") }));
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
            title: t("settings.rotate.started"),
            detail: t("settings.rotate.started.detail"),
          });
          onDone();
        },
        onError: (rotateError) =>
          toast.push({
            tone: "danger",
            title: isRevisionConflict(rotateError)
              ? t("settings.conflict")
              : t("settings.rotate.refused"),
            detail: isRevisionConflict(rotateError)
              ? t("settings.conflict.reenter")
              : describeError(rotateError, t),
          }),
      },
    );
  };

  return (
    <Panel
      title={t("settings.rotateCredential")}
      meta={`${venue.credentialSchema.authKind} · v${venue.credentialSchema.schemaVersion}`}
    >
      <form onSubmit={submit} className="flex max-w-lg flex-col gap-3">
        <p className="text-meta text-dim">{t("settings.rotate.hint")}</p>
        <CredentialFields
          venue={venue}
          values={values}
          onChange={(key, value) => setValues((current) => ({ ...current, [key]: value }))}
          disabled={rotate.isPending}
        />
        {error ? <p className="text-meta text-danger">{error}</p> : null}
        <div className="flex items-center gap-2">
          <Button type="submit" variant="primary" size="sm" disabled={rotate.isPending}>
            {rotate.isPending ? t("settings.rotating") : t("settings.rotate")}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={onDone}>
            {t("settings.cancel")}
          </Button>
        </div>
      </form>
    </Panel>
  );
}

/**
 * Both products the gateway recognises, and not a control.
 *
 * An account is entitled to the whole terminal or it is not worth adding: the
 * entitlement exists so a product service can refuse an account it was never
 * meant to route, not so an operator can hide half of their own screen. An
 * account created before arbitrage existed carries only `manual` and will not
 * appear in the hedge picker until it is added again.
 */
const PRODUCT_POLICY = ["manual", "arbitrage"];

function CreateAccount({ venues, onCreated }: { venues: Venue[]; onCreated: (account: Account) => void }) {
  const toast = useToast();
  const create = useCreateAccount();
  const t = useT();
  const [venueName, setVenueName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [routeIds, setRouteIds] = useState<string[]>([]);
  const [values, setValues] = useState<CredentialValues>({});
  const [error, setError] = useState<string | null>(null);

  const venue = venues.find((candidate) => candidate.venue === venueName) ?? null;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!venue) {
      setError(t("settings.venue.required"));
      return;
    }
    if (displayName.trim() === "") {
      setError(t("settings.displayName.required"));
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
      setError(t("settings.routes.required"));
      return;
    }
    const missing = missingCredentialFields(venue, values);
    if (missing.length > 0) {
      setError(t("settings.stillNeeded", { fields: missing.join(", ") }));
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
          toast.push({ tone: "ok", title: t("settings.created"), detail: account.accountId });
          onCreated(account);
        },
        onError: (createError) =>
          toast.push({ tone: "danger", title: t("settings.refused"), detail: describeError(createError, t) }),
      },
    );
  };

  return (
    <Panel title={t("settings.newAccount")}>
      <form onSubmit={submit} className="flex max-w-lg flex-col gap-3">
        <Field label={t("settings.venue")} required hint={t("settings.venue.hint")}>
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
              <option value="">{t("settings.chooseVenue")}</option>
              {venues.map((candidate) => (
                <option key={candidate.venue} value={candidate.venue}>
                  {candidate.venue} · {candidate.nativeEnvironment}
                  {candidate.ready ? "" : ` ${t("settings.venueNotReady")}`}
                </option>
              ))}
            </select>
          )}
        </Field>

        <Field label={t("settings.displayName")} required>
          {(field) => (
            <Input
              {...field}
              value={displayName}
              autoComplete="off"
              placeholder={t("settings.displayName.placeholder")}
              onChange={(event) => setDisplayName(event.target.value)}
            />
          )}
        </Field>

        {venue ? (
          <>
            <fieldset className="flex flex-col gap-1.5">
              <legend className="th">
                {t("settings.routes")} <span className="text-danger" aria-hidden="true">*</span>
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
                <p className="text-meta text-dim">{t("settings.noRouteProfiles")}</p>
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
            {create.isPending ? t("settings.creating") : t("settings.createAccount")}
          </Button>
          <span className="text-meta text-dim">{t("settings.manualOnly")}</span>
        </div>
      </form>
    </Panel>
  );
}
