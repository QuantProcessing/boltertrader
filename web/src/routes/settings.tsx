import { Bell, KeyRound, LockKeyhole, MessageCircle, Plus, Smartphone, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { describeError } from "@/api/errors";
import type { Account, Venue } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Panel } from "@/components/ui/panel";
import { StateBadge, Tag } from "@/components/ui/state-badge";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { buildCredentialBundle, CredentialFields, invalidCredentialFields, missingCredentialFields, type CredentialValues } from "@/features/settings/credential-form";
import { useAccounts } from "@/features/market/queries";
import { useAccountRuntimes, useCreateAccount, useDeleteNotification, useNotifications, useRemoveAccount, useSaveNotification, useTestNotification, useVenues } from "@/features/settings/queries";
import type { NotificationChannel, NotificationProvider } from "@/api/types";
import { formatTimestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export function SettingsPage() {
  const readOnly = useReadOnly();
  const search = useSearch({ from: "/app/settings" });

  // A separate child is load-bearing here: the account hooks below must never
  // mount and briefly expose cached private account data while this tab is
  // signed out. Conditional calls inside one component would violate the
  // Rules of Hooks when the session changes.
  return readOnly ? <ReadOnlySettings /> : <SettingsWorkspace initialSection={search.section ?? "notifications"} />;
}

function ReadOnlySettings() {
  const navigate = useNavigate();
  const t = useT();

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="workspace-page-header flex h-bar shrink-0 items-center border-b border-line px-3">
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

function SettingsWorkspace({ initialSection }: { initialSection: "notifications" | "accounts" }) {
  const t = useT();
  const accountsQuery = useAccounts();
  const venuesQuery = useVenues();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [section, setSection] = useState<"notifications" | "accounts">(initialSection);

  const list = accountsQuery.data ?? [];
  const venues = venuesQuery.data?.items ?? [];
  const selected = list.find((account) => account.accountId === selectedId) ?? list[0] ?? null;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="workspace-page-header flex h-bar shrink-0 items-center gap-3 border-b border-line px-3">
        <div>
          <h1 className="text-title font-medium text-text">{t(section === "notifications" ? "settings.notifications" : "settings.accounts")}</h1>
          <p className="text-meta text-dim">{t(section === "notifications" ? "settings.notifications.detail" : "settings.permissionHint")}</p>
        </div>
        <div className="ml-auto flex items-center gap-1 rounded-card bg-fill p-1">
          <Button variant={section === "notifications" ? "outline" : "ghost"} size="sm" onClick={() => setSection("notifications")}><Bell className="size-3.5" />{t("settings.notifications")}</Button>
          <Button variant={section === "accounts" ? "outline" : "ghost"} size="sm" onClick={() => setSection("accounts")}><KeyRound className="size-3.5" />{t("settings.accounts")}</Button>
        </div>
        {section === "accounts" ? <Tag>{list.length}</Tag> : null}
        <Button
          variant={creating ? "outline" : "primary"}
          size="sm"
          className={section === "accounts" ? "" : "hidden"}
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

      {section === "notifications" ? <NotificationSettings /> : <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
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
      </div>}
    </div>
  );
}

function NotificationSettings() {
  const t = useT();
  const query = useNotifications();
  const channels = query.data ?? [];
  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-3 lg:p-5">
      {query.isError ? <p role="alert" className="mb-4 rounded-card border border-danger/40 bg-danger/10 p-3 text-danger">{t("settings.notifications.unavailable")}</p> : null}
      <div className="settings-notification-grid">
        <NotificationCard provider="telegram" channel={channels.find((item) => item.provider === "telegram")} />
        <NotificationCard provider="bark" channel={channels.find((item) => item.provider === "bark")} />
      </div>
    </div>
  );
}

function NotificationCard({ provider, channel }: { provider: NotificationProvider; channel?: NotificationChannel }) {
  const t = useT();
  const save = useSaveNotification();
  const remove = useDeleteNotification();
  const test = useTestNotification();
  const [secret, setSecret] = useState("");
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [chatId, setChatId] = useState(channel?.chatId ?? "");
  const [serverUrl, setServerUrl] = useState(channel?.serverUrl ?? "https://api.day.app");
  const [critical, setCritical] = useState(channel?.critical ?? false);
  const [volume, setVolume] = useState(channel?.volume || 5);
  const Icon = provider === "telegram" ? MessageCircle : Smartphone;
  const isBark = provider === "bark";
  const title = provider === "telegram" ? t("settings.notifications.telegram") : t("settings.notifications.bark");
  const detail = provider === "telegram" ? t("settings.notifications.telegram.detail") : t("settings.notifications.bark.detail");
  const saveChannel = () => save.mutate({ provider, input: { secret: secret || undefined, enabled, chatId: isBark ? undefined : chatId, serverUrl: isBark ? serverUrl : undefined, critical: isBark ? critical : false, volume: isBark ? volume : 0 } }, { onSuccess: () => setSecret("") });
  return (
    <section className="settings-notification-panel">
      <header className="flex items-start gap-3 border-b border-line px-4 py-3">
        <span className="flex size-9 items-center justify-center rounded-card bg-accent-soft text-accent"><Icon className="size-4" /></span>
        <div><h2 className="text-base font-semibold text-text">{title}</h2><p className="text-meta text-dim">{detail}</p></div>
        <Tag className="ml-auto">{channel ? t("settings.status.active") : t("settings.notifications.none")}</Tag>
      </header>
      <div className="flex flex-col gap-4 p-4">
        <Field label={t(isBark ? "settings.notifications.deviceKey" : "settings.notifications.botToken")} hint={t("settings.notifications.secretHint")}>
          {(field) => <Input {...field} type="password" value={secret} autoComplete="new-password" onChange={(event) => setSecret(event.target.value)} placeholder={channel ? "••••••••" : ""} />}
        </Field>
        {isBark ? <Field label={t("settings.notifications.server")} hint={t("settings.notifications.serverHint")}>
          {(field) => <Input {...field} value={serverUrl} onChange={(event) => setServerUrl(event.target.value)} />}
        </Field> : <Field label={t("settings.notifications.chatId")} hint={t("settings.notifications.telegramHint")}>
          {(field) => <Input {...field} value={chatId} onChange={(event) => setChatId(event.target.value)} placeholder="-100… or @channel" />}
        </Field>}
        <div className="flex items-center justify-between border-t border-line pt-3"><div><label htmlFor={`${provider}-enabled`} className="text-base font-medium text-text">{t("settings.notifications.enabled")}</label></div><Switch id={`${provider}-enabled`} checked={enabled} onCheckedChange={setEnabled} /></div>
        {isBark ? <><div className="flex items-start justify-between gap-3"><div><label htmlFor="bark-critical" className="text-base font-medium text-text">{t("settings.notifications.critical")}</label><p className="text-meta text-dim">{t("settings.notifications.critical.detail")}</p></div><Switch id="bark-critical" checked={critical} onCheckedChange={setCritical} /></div>{critical ? <label className="flex items-center gap-3 text-meta text-muted">{t("settings.notifications.volume")}<input className="flex-1 accent-accent" type="range" min="0" max="10" value={volume} onChange={(event) => setVolume(Number(event.target.value))} /><span className="w-5 text-right tabular-nums">{volume}</span></label> : null}</> : null}
        {isBark ? <p className="text-meta text-dim">{t("settings.notifications.barkHint")}</p> : null}
        {save.isError || test.isError || remove.isError ? <p role="alert" className="text-meta text-danger">{describeError(save.error ?? test.error ?? remove.error, t)}</p> : null}
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="primary" size="sm" onClick={saveChannel} disabled={save.isPending}>{save.isPending ? t("settings.creating") : t("settings.notifications.save")}</Button>
          {channel ? <Button variant="outline" size="sm" onClick={() => test.mutate(provider)} disabled={test.isPending}>{test.isPending ? t("settings.notifications.testing") : t("settings.notifications.test")}</Button> : null}
          {channel ? <Button variant="ghost" size="sm" className="ml-auto text-danger" onClick={() => remove.mutate(provider)} disabled={remove.isPending}><Trash2 className="size-3.5" />{t("settings.notifications.remove")}</Button> : null}
        </div>
        {test.isSuccess ? <p role="status" className="text-meta text-ok">{t("settings.notifications.tested")}</p> : null}
      </div>
    </section>
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
