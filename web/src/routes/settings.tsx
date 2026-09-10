import { KeyRound, LockKeyhole, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { describeError } from "@/api/errors";
import type { Account, Venue } from "@/api/types";
import { modeStore, useReadOnly } from "@/app/mode";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Panel } from "@/components/ui/panel";
import { StateBadge, Tag } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import { buildCredentialBundle, CredentialFields, invalidCredentialFields, missingCredentialFields, type CredentialValues } from "@/features/settings/credential-form";
import { useAccounts } from "@/features/market/queries";
import { useAccountRuntimes, useCreateAccount, useRemoveAccount, useVenues } from "@/features/settings/queries";
import { formatTimestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

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
  const accountsQuery = useAccounts();
  const venuesQuery = useVenues();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const list = accountsQuery.data ?? [];
  const venues = venuesQuery.data?.items ?? [];
  const selected = list.find((account) => account.accountId === selectedId) ?? list[0] ?? null;

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

      <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
        <nav aria-label={t("settings.accounts")} className="max-h-48 shrink-0 overflow-y-auto border-b border-line sm:max-h-none sm:w-72 sm:border-r sm:border-b-0">
          {accountsQuery.isError ? <p role="alert" className="p-3 text-danger">{describeError(accountsQuery.error, t)}</p> : null}
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
                account.accountId === selected?.accountId && !creating ? "bg-raised" : "",
              )}
            >
              <span className="flex items-center gap-2">
                <span className="min-w-0 truncate text-base text-text">{account.displayName}</span>
                <StateBadge
                  tone="ok"
                  label={t("settings.status.active")}
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
          {venuesQuery.isError && creating ? <p role="alert" className="text-danger">{describeError(venuesQuery.error, t)}</p> : null}
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

function AccountDetail({ account }: { account: Account }) {
 const t = useT();
 const runtimes = useAccountRuntimes(account.accountId);
 const remove = useRemoveAccount();
 const [confirming, setConfirming] = useState(false);
 const connected = runtimes.data?.[0]?.connected === true;
 return <Panel title={account.displayName}>
  <dl className="grid grid-cols-2 gap-x-6 gap-y-3 text-base">
   <dt className="text-muted">{t("settings.venue")}</dt><dd>{account.venue}</dd>
   <dt className="text-muted">{t("settings.lastValidated")}</dt><dd>{account.lastValidatedAt ? formatTimestamp(account.lastValidatedAt) : t("settings.never")}</dd>
   <dt className="text-muted">{t("settings.connection")}</dt><dd role="status"><StateBadge tone={connected ? "ok" : "neutral"} label={runtimes.isPending ? t("settings.runtime.loading") : connected ? t("settings.connected") : t("settings.disconnected")} /></dd>
  </dl>
  {runtimes.isError ? <p role="alert" className="mt-3 text-meta text-danger">{describeError(runtimes.error,t)}</p> : null}
  <p className="mt-4 max-w-prose text-meta text-muted">{t("settings.permissionHint")}</p>
  <div className="mt-5 flex flex-wrap items-center gap-3">
   {confirming ? <>
    <p className="basis-full text-meta text-muted">{t("settings.remove.confirm",{name:account.displayName})}</p>
    <Button size="sm" variant="danger" disabled={remove.isPending} onClick={() => remove.mutate(account)}>{remove.isPending ? t("settings.removing") : t("settings.remove")}</Button>
    <Button size="sm" variant="ghost" disabled={remove.isPending} onClick={() => setConfirming(false)}>{t("settings.keep")}</Button>
   </> : <Button size="sm" variant="ghost" onClick={() => setConfirming(true)}>{t("settings.remove")}</Button>}
  </div>
  {remove.isError ? <p role="alert" className="mt-3 text-danger">{describeError(remove.error,t)}</p> : null}
 </Panel>;
}

function CreateAccount({ venues, onCreated }: { venues: Venue[]; onCreated: (account: Account) => void }) {
  const toast = useToast();
  const create = useCreateAccount();
  const t = useT();
  const [venueName, setVenueName] = useState("");
  const [displayName, setDisplayName] = useState("");
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
        credentialBundle: buildCredentialBundle(venue, values),
      },
      {
        onSettled: () => { setValues({}); create.reset(); },
        onSuccess: (account) => {
          toast.push({ tone: "ok", title: t("settings.created"), detail: account.accountId });
          onCreated(account);
        },
        onError: (createError) => setError(describeError(createError, t)),
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
              disabled={create.isPending}
              onChange={(event) => {
                setVenueName(event.target.value);
                // Routes and credential fields belong to the venue that was
                // showing when they were entered. Keeping them would submit one
                // venue's key under another's schema.
                setValues({});
                setError(null);
              }}
            >
              <option value="">{t("settings.chooseVenue")}</option>
              {venues.map((candidate) => (
                <option key={candidate.venue} value={candidate.venue}>
                  {candidate.venue}
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
              disabled={create.isPending}
              autoComplete="off"
              placeholder={t("settings.displayName.placeholder")}
              onChange={(event) => setDisplayName(event.target.value)}
            />
          )}
        </Field>

        {venue ? (
          <>
            <CredentialFields
              venue={venue}
              values={values}
              onChange={(key, value) => setValues((current) => ({ ...current, [key]: value }))}
              disabled={create.isPending}
            />
          </>
        ) : null}

        {error ? <p role="alert" className="text-meta text-danger">{error}</p> : null}

        <div className="flex items-center gap-2">
          <Button type="submit" variant="primary" size="sm" disabled={create.isPending || !venue}>
            {create.isPending ? t("settings.creating") : t("settings.createAccount")}
          </Button>
          <span className="text-meta text-dim">{t("settings.verifyHint")}</span>
        </div>
      </form>
    </Panel>
  );
}
