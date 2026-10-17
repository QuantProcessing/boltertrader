import * as Popover from "@radix-ui/react-popover";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { ApiError, api } from "@/api/client";
import type { VenueInfo } from "@/api/types";
import { useWorkspace } from "@/state/workspace";
import { Dialog, PrimaryButton, cx } from "@/ui/primitives";
import { venueLabel } from "./market-header";

const HINTS: Record<string, string> = {
  binance: "Create an API key with Futures + Spot trading enabled. Do not enable withdrawals.",
  okx: "Create an API key with Trade permission. Do not enable withdrawals.",
  bybit: "Create an API key with Contract + Spot trade permission. Do not enable withdrawals.",
  bitget: "Create an API key with trade permission. Do not enable withdrawals.",
  gate: "Create an API key with futures and spot trade permission. Do not enable withdrawals.",
  hyperliquid: "Use an API wallet private key (app.hyperliquid.xyz/API). Set Account address to your main wallet.",
  lighter: "Use an API key private key and its key index; account index is shown in your Lighter account.",
};

/** The only place credentials are entered. Secrets live only in this form's state until submit. */
export function ConnectDialog() {
  const { connectOpen, setConnectOpen, setAccountId } = useWorkspace();
  const queryClient = useQueryClient();
  const venues = useQuery({ queryKey: ["venues"], queryFn: api.venues, staleTime: Infinity, enabled: connectOpen });
  const [venue, setVenue] = useState<VenueInfo | null>(null);
  const [label, setLabel] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ text: string; network: boolean } | null>(null);

  useEffect(() => {
    if (!connectOpen) {
      setVenue(null);
      setValues({});
      setLabel("");
      setError(null);
    }
  }, [connectOpen]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!venue) return;
    setBusy(true);
    setError(null);
    try {
      const account = await api.createAccount(venue.venue, label, values);
      setValues({});
      await queryClient.invalidateQueries({ queryKey: ["accounts"] });
      setAccountId(account.id);
      setConnectOpen(false);
    } catch (e) {
      // A network failure says nothing about the key: keep the form filled
      // so the user can simply retry.
      const network = e instanceof ApiError && (e.category === "network" || e.category === "venue_error" || e.category === "rate_limited");
      setError({ text: (e as Error).message, network });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={connectOpen}
      onOpenChange={setConnectOpen}
      title={venue ? `Connect ${venueLabel(venue.venue)}` : "Connect Exchange"}
      description={venue ? HINTS[venue.venue] : "Choose the exchange to trade on. Keys are verified against the exchange, then stored encrypted on this machine."}
    >
      {!venue ? (
        <div className="grid grid-cols-2 gap-2">
          {(venues.data ?? []).map((item) => (
            <button key={item.venue} onClick={() => setVenue(item)} className="flex h-12 flex-col items-start justify-center rounded-lg bg-raised px-3 text-left hover:bg-line-strong/40">
              <span className="text-md">{venueLabel(item.venue)}</span>
              <span className="text-muted">{item.markets.map((m) => (m.includes(":perp") ? "Perp" : "Spot") + (m.split(":")[2] ? " " + m.split(":")[2] : "")).join(" · ")}</span>
            </button>
          ))}
          {venues.isLoading && <div className="col-span-2 py-6 text-center text-muted">Loading…</div>}
          {venues.error && <div className="col-span-2 py-6 text-center text-down">{(venues.error as Error).message}</div>}
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-2.5" autoComplete="off">
          {venue.fields.map((field) => (
            <label key={field.key} className="flex flex-col gap-1">
              <span className="text-muted">{field.label}{field.required ? "" : " (optional)"}</span>
              <input
                type={field.metadata || field.valueType === "integer" ? "text" : "password"}
                inputMode={field.valueType === "integer" ? "numeric" : undefined}
                autoComplete="off"
                spellCheck={false}
                required={field.required}
                value={values[field.key] ?? ""}
                onChange={(e) => setValues((current) => ({ ...current, [field.key]: e.target.value.trim() }))}
                className="h-9 rounded-lg border border-line bg-transparent px-3 outline-none focus:border-line-strong"
              />
            </label>
          ))}
          <label className="flex flex-col gap-1">
            <span className="text-muted">Label (optional)</span>
            <input value={label} maxLength={64} onChange={(e) => setLabel(e.target.value)} placeholder={venueLabel(venue.venue)} className="h-9 rounded-lg border border-line bg-transparent px-3 outline-none placeholder:text-faint focus:border-line-strong" />
          </label>
          <PrimaryButton type="submit" disabled={busy} className="mt-3">
            {busy ? "Verifying with exchange…" : error?.network ? "Retry" : "Connect"}
          </PrimaryButton>
          {error && <div className={cx("text-center", error.network ? "text-text-2" : "text-down")}>{error.text}</div>}
          <button type="button" onClick={() => { setVenue(null); setValues({}); setError(null); }} className="text-muted hover:text-text">← Choose another exchange</button>
        </form>
      )}
    </Dialog>
  );
}

export function AccountSwitcher() {
  const { accounts, account, setAccountId, setConnectOpen, live } = useWorkspace();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  if (!account) {
    return <button onClick={() => setConnectOpen(true)} className="h-8 rounded-lg bg-accent px-5 text-on-accent hover:bg-accent-hover">Connect</button>;
  }

  const remove = async (id: string) => {
    await api.deleteAccount(id);
    setConfirming(null);
    await queryClient.invalidateQueries({ queryKey: ["accounts"] });
  };

  return (
    <Popover.Root open={open} onOpenChange={(value) => { setOpen(value); if (!value) setConfirming(null); }}>
      <Popover.Trigger className="flex h-8 items-center gap-2 rounded-lg border border-line px-3 hover:bg-raised">
        <span className={cx("size-1.5 rounded-full", live.status?.connected ? "bg-up" : "bg-down")} />
        <span>{venueLabel(account.venue)}</span>
        <span className="text-muted">{account.label !== account.venue ? account.label : ""}</span>
        <span className="text-muted">▾</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} className="z-40 w-72 rounded-[10px] border border-line bg-overlay p-1 shadow-2xl">
          {accounts.map((item) => (
            <div key={item.id} className={cx("flex items-center gap-2 rounded-md px-2 py-2", item.id === account.id && "bg-raised")}>
              <button className="flex flex-1 flex-col items-start" onClick={() => { setAccountId(item.id); setOpen(false); }}>
                <span>{venueLabel(item.venue)} <span className="text-muted">{item.label !== item.venue ? item.label : ""}</span></span>
                <span className="text-xs text-muted">{item.id}{item.active ? "" : " · inactive"}</span>
              </button>
              {confirming === item.id ? (
                <span className="flex gap-2">
                  <button className="text-down" onClick={() => remove(item.id)}>Remove</button>
                  <button className="text-muted" onClick={() => setConfirming(null)}>Keep</button>
                </span>
              ) : (
                <button className="text-muted hover:text-down" aria-label={`Remove ${item.label}`} onClick={() => setConfirming(item.id)}>✕</button>
              )}
            </div>
          ))}
          <button className="mt-1 w-full rounded-md px-2 py-2 text-left text-accent hover:bg-raised" onClick={() => { setOpen(false); setConnectOpen(true); }}>
            + Connect exchange
          </button>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
