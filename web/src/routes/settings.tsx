import { KeyRound, LockKeyhole, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";

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
import { modeStore, useReadOnly } from "@/app/mode";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Panel } from "@/components/ui/panel";
import { StateBadge, Tag, type StateTone } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import {
  buildCredentialBundle,
  CredentialFields,
  invalidCredentialFields,
  missingCredentialFields,
  type CredentialValues,
} from "@/features/settings/credential-form";
import { useAccounts } from "@/features/market/queries";
import {
  useAccountRuntimes,
  useCreateAccount,
  useDisableAccount,
  useRemoveAccount,
  useVenues,
} from "@/features/settings/queries";
import { formatTimestamp } from "@/lib/format";
import { useT, type Key, type Translate } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * Settings: the accounts, and what may be done to them.
 *
 * The gateway record says what was requested; the runtime says whether the
 * immutable account registration is currently usable. Internal runtime
 * identities and counters are deliberately not operator-facing concepts.
 *
 * Forms expand inline rather than opening dialogs: no dialog component exists
 * here, and inline keeps the account being modified visible while it is edited.
 * ------------------------------------------------------------------------- */

const STATUS_TONE: Record<AccountStatus, StateTone> = {
  creating: "pending",
  active: "ok",
  disabled: "neutral",
  removing: "warn",
  removed: "neutral",
};

const ACCOUNT_STATUS_KEYS: Record<AccountStatus, Key> = {
  creating: "settings.status.creating",
  active: "settings.status.active",
  disabled: "settings.status.disabled",
  removing: "settings.status.removing",
  removed: "settings.status.removed",
};

const RUNTIME_STATE_KEYS: Partial<Record<string, Key>> = {
  prepared: "settings.runtimeState.prepared",
  candidate_ready: "settings.runtimeState.candidateReady",
  candidate_failed: "settings.runtimeState.candidateFailed",
  applied: "settings.runtimeState.applied",
};

function accountStatusLabel(status: AccountStatus, t: Translate): string {
  return t(ACCOUNT_STATUS_KEYS[status]);
}

function requestedStateLabel(state: string, t: Translate): string {
  const key = (ACCOUNT_STATUS_KEYS as Record<string, Key>)[state];
  return key ? t(key) : state;
}

function runtimeStateLabel(state: string, t: Translate): string {
  const key = RUNTIME_STATE_KEYS[state];
  return key ? t(key) : state;
}

/** Nothing may be asked of an account the gateway is already tearing down. */
const isTerminal = (status: AccountStatus): boolean => status === "removing" || status === "removed";

const RUNTIME_REASON_KEYS: Partial<Record<string, Key>> = {
  CREDENTIAL_EXPIRED: "settings.reason.credentialExpired",
  CREDENTIAL_INVALID: "settings.reason.credentialInvalid",
  CREDENTIAL_PERMISSION_DENIED: "settings.reason.credentialPermissionDenied",
  VENUE_RATE_LIMITED: "settings.reason.venueRateLimited",
  VENUE_UNAVAILABLE: "settings.reason.venueUnavailable",
  VENUE_RESPONSE_INVALID: "settings.reason.venueResponseInvalid",
  "live private writes disabled": "settings.reason.liveWritesDisabled",
  private_stream_connect_failed: "settings.reason.privateStreamConnectFailed",
  private_stream_starting: "settings.reason.privateStreamStarting",
  capability_not_promoted: "settings.reason.capabilityNotPromoted",
  capability_write_disabled: "settings.reason.capabilityNotPromoted",
  exact_lookup_unavailable: "settings.reason.exactLookupUnavailable",
  CANDIDATE_BUILD_FAILED: "settings.reason.candidateBuildFailed",
};

function describeRuntimeReason(reason: string | undefined, t: Translate): string | undefined {
  if (!reason) return undefined;
  const key = RUNTIME_REASON_KEYS[reason];
  return key ? t(key) : reason;
}

export function SettingsPage() {
  const readOnly = useReadOnly();

  // A separate child is load-bearing here: the account hooks below must never
  // mount and briefly expose cached private account data while this tab is
  // signed out. Conditional calls inside one component would violate the
  // Rules of Hooks when the session changes.
  return readOnly ? <ReadOnlySettings /> : <SettingsWorkspace />;
}

function ReadOnlySettings() {
  const navigate = useNavigate();
  const t = useT();

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-bar shrink-0 items-center border-b border-line px-3">
        <h1 className="text-title font-medium text-text">{t("settings.accounts")}</h1>
      </header>
      <EmptyState
        icon={LockKeyhole}
        title={t("settings.readOnly.title")}
        detail={t("settings.readOnly.anonymous")}
        className="min-h-0 flex-1"
        action={
          <Button
            variant="primary"
            size="sm"
            onClick={() => {
              modeStore.choose("readWrite");
              void navigate({ to: "/login", search: { redirect: "/settings" } });
            }}
          >
            {t("settings.readOnly.signIn")}
          </Button>
        }
      />
    </div>
  );
}

function SettingsWorkspace() {
  const t = useT();
  const accountsQuery = useAccounts({ watchLifecycle: true });
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
                  label={accountStatusLabel(account.status, t)}
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

function AccountDetail({ account }: { account: Account; venue: Venue | null }) {
  const toast = useToast();
  const t = useT();
  const runtimes = useAccountRuntimes(account.accountId);
  const [confirmingRemove, setConfirmingRemove] = useState(false);

  const disable = useDisableAccount();
  const remove = useRemoveAccount();

  const busy = disable.isPending || remove.isPending;
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
        title: t(failed),
        detail: describeError(error, t),
      }),
  });

  return (
    <div className="flex flex-col gap-3">
      <Panel
        title={account.displayName}
        meta={
          <StateBadge
            tone={STATUS_TONE[account.status]}
            label={accountStatusLabel(account.status, t)}
          />
        }
      >
        <AccountActivationStatus account={account} runtime={runtime} loading={runtimes.isPending} />

        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-meta md:grid-cols-3">
          <Fact label={t("settings.venue")} value={account.venue} />
          <Fact label={t("settings.products")} value={account.productPolicy.join(", ")} />
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
                disabled={busy || terminal}
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

function AccountActivationStatus({
  account,
  runtime,
  loading,
}: {
  account: Account;
  runtime: AccountRuntime | null;
  loading: boolean;
}) {
  const t = useT();
  const pendingState = runtime?.pending?.state;
  const failed = pendingState === "candidate_failed";
  // A multi-route account can have a promoted trading route next to an
  // intentionally read-only route. That is partial capability, not a failed
  // account: the exact route still gates every write at execution time.
  const hasWritableRoute = runtime?.capabilities.some((capability) => capability.writeReady) === true;
  const operational = runtime?.readiness.ready === true && hasWritableRoute;
  const shouldShow =
    account.status === "creating" ||
    account.status === "removing" ||
    (account.status === "active" && !operational);

  if (!shouldShow) return null;

  let title = t("settings.setup.validating");
  let fallbackDetail = t("settings.setup.validating.detail");
  let tone: StateTone = "pending";
  let completed = 1;
  let failedStep: number | null = null;

  if (failed) {
    title = t("settings.setup.failed");
    fallbackDetail = t("settings.setup.failed.detail");
    tone = "danger";
    failedStep = 1;
  } else if (account.status === "creating" && (pendingState === "candidate_ready" || runtime?.active)) {
    title = t("settings.setup.enabling");
    fallbackDetail = t("settings.setup.enabling.detail");
    completed = 2;
  } else if (account.status === "active") {
    title = runtime?.blockedReason ? t("settings.setup.blocked") : t("settings.setup.syncing");
    fallbackDetail = runtime?.blockedReason
      ? t("settings.setup.blocked.detail")
      : t("settings.setup.syncing.detail");
    tone = runtime?.blockedReason ? "warn" : "pending";
    completed = 2;
  } else if (account.status === "removing") {
    title = t("settings.setup.removing");
    fallbackDetail = t("settings.setup.removing.detail");
    tone = "warn";
    completed = 2;
  }

  const reason =
    describeRuntimeReason(runtime?.pending?.reason, t) ??
    describeRuntimeReason(runtime?.blockedReason ?? undefined, t) ??
    describeRuntimeReason(runtime?.writeReadiness.reason, t) ??
    describeRuntimeReason(runtime?.readiness.reason, t);
  const steps = [
    t("settings.setup.step.saved"),
    t("settings.setup.step.verified"),
    t("settings.setup.step.enabled"),
  ];

  return (
    <div
      className="mb-3 rounded-card bg-fill px-3 py-2"
      role="status"
      aria-live="polite"
      aria-atomic="true"
    >
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge tone={tone} label={title} />
        <span className="text-meta text-muted">
          {loading && !runtime ? t("settings.runtime.loading") : reason ?? fallbackDetail}
        </span>
      </div>
      {account.status === "creating" ? (
        <ol className="mt-2 grid grid-cols-3 gap-2" aria-label={t("settings.setup.progress")}>
          {steps.map((step, index) => {
            const done = index < completed;
            const isFailed = failedStep === index;
            const current = !done && !isFailed && index === completed;
            return (
              <li
                key={step}
                className={cn(
                  "flex min-w-0 items-center gap-1.5 text-meta",
                  done ? "text-muted" : current || isFailed ? "text-text" : "text-dim",
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "size-2 shrink-0 rounded-full",
                    done ? "bg-ok" : isFailed ? "bg-danger" : current ? "bg-pending" : "bg-line-strong",
                  )}
                />
                <span className="truncate">{step}</span>
              </li>
            );
          })}
        </ol>
      ) : null}
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

  const writableRouteCount = runtime.capabilities.filter((capability) => capability.writeReady).length;
  const anyWritable = writableRouteCount > 0;
  const partiallyWritable = anyWritable && writableRouteCount < runtime.capabilities.length;
  const writeDetail = partiallyWritable
    ? t("settings.writePartiallyReady.detail", {
        ready: writableRouteCount,
        total: runtime.capabilities.length,
      })
    : describeRuntimeReason(runtime.writeReadiness.reason, t);

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
          ready={runtime.writeReadiness.ready || anyWritable}
          state={partiallyWritable ? t("settings.partiallyReady") : undefined}
          detail={writeDetail}
        />
      </div>

      {runtime.blockedReason !== null && !anyWritable ? (
        <p className="mt-2 text-meta text-warn">
          {t("settings.blocked", {
            reason: describeRuntimeReason(runtime.blockedReason, t) ?? runtime.blockedReason,
          })}
        </p>
      ) : null}

      <div className="mt-3 flex flex-col gap-3 border-t border-line pt-3">
        {runtime.active ? (
          <Generation title={t("settings.active")} snapshot={runtime.active} tone="ok" />
        ) : null}
        {runtime.pending ? (
          <Generation title={t("settings.pending")} snapshot={runtime.pending} tone="pending" />
        ) : null}
        {runtime.retiring.map((snapshot, index) => (
          <Generation
            key={`retiring-${index}`}
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

function Readiness({
  label,
  ready,
  reason,
  state,
  detail,
}: {
  label: string;
  ready: boolean;
  reason?: string;
  state?: string;
  detail?: string;
}) {
  const t = useT();
  return (
    <StateBadge
      tone={ready ? "ok" : "warn"}
      // One interpolated key rather than `${label}: ${state}` assembled here:
      // the separator is a full-width colon in Chinese, and a half-width one
      // between two Chinese words reads as a typesetting mistake.
      label={t("settings.readiness", {
        label,
        state: state ?? (ready ? t("settings.ready") : t("settings.notReady")),
      })}
      detail={detail ?? describeRuntimeReason(reason, t)}
      className="text-base"
    />
  );
}

/** One internal connection snapshot used while creating, serving, or removing. */
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
        {/* The caller labels whether this connection is active, pending, or retiring. */}
        <StateBadge tone={tone} label={title} className="text-base text-text" />
        {/* `requestedState`, the pending `state` and `reason` are the runtime's
            own words and stay in them, same as the blotter's command states. */}
        <Tag>{requestedStateLabel(snapshot.requestedState, t)}</Tag>
        {pending ? <Tag>{runtimeStateLabel(pending.state, t)}</Tag> : null}
      </div>
      {pending && pending.reason !== "" ? (
        <p className="mt-1 text-meta text-muted">{describeRuntimeReason(pending.reason, t)}</p>
      ) : null}
      <ul className="mt-1 flex flex-col">
        {snapshot.routes.map((route) => (
          <RuntimeRouteRow key={route.routeId} route={route} />
        ))}
      </ul>
    </div>
  );
}

function RuntimeRouteRow({ route }: { route: RuntimeSnapshot["routes"][number] }) {
  const t = useT();
  // A write-policy gate does not mean the route connection failed. The
  // capability section below remains the source of truth for whether it can
  // accept private writes.
  const connected = route.ready || route.reason === "capability_not_promoted";
  return (
    <li className="flex h-row items-center gap-3 text-meta">
      <span className="min-w-0 flex-1 truncate text-base">{route.routeId}</span>
      <span className="shrink-0 text-dim">{route.product}</span>
      <StateBadge
        tone={connected ? "ok" : "warn"}
        label={connected ? t("settings.ready") : t("settings.notReady")}
        detail={describeRuntimeReason(route.reason, t)}
        className="w-24 shrink-0 justify-end text-muted"
      />
    </li>
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
    const invalid = invalidCredentialFields(venue, values);
    if (invalid.length > 0) {
      setError(t("settings.invalidFields", { fields: invalid.join(", ") }));
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
