import { useEffect, useState } from "react";

import { describeError } from "@/api/errors";
import type { Account, Instrument, OrderSide, OrderType } from "@/api/types";
import { MANUAL_PREVIEW_TTL_MS } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Panel } from "@/components/ui/panel";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { usePlaceOrder, usePreviewOrder, type SealedPreview } from "@/features/trade/queries";
import { formatDecimal, formatDuration, splitDecimal } from "@/lib/format";
import { useT, type Key } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * The order ticket.
 *
 * Two steps, because the server has two: a preview seals the priced intent and
 * a command acts on the seal. The seal expires after thirty seconds, and the
 * deadline is inside the token rather than in the response — so the countdown
 * here starts when the response landed, which makes it a lower bound. Being
 * early is the safe direction: the worst case is asking for a fresh preview
 * that was still technically valid.
 *
 * Blockers are the server's refusals and are absolute — the confirm button does
 * not exist while there is one. Warnings are advisory and confirm stays live.
 * ------------------------------------------------------------------------- */

/**
 * The two static option lists name their label instead of holding it: a
 * module-level array outlives every render, so the dictionary lookup has to
 * happen at the call site where the locale is known.
 */
const SIDES: { value: OrderSide; labelKey: Key; tone: "buy" | "sell" }[] = [
  { value: "buy", labelKey: "ticket.side.buy", tone: "buy" },
  { value: "sell", labelKey: "ticket.side.sell", tone: "sell" },
];

const ORDER_TYPES: { value: OrderType; labelKey: Key }[] = [
  { value: "limit", labelKey: "ticket.type.limit" },
  { value: "market", labelKey: "ticket.type.market" },
];

/** The confirm button and the success toast, which spell the side differently. */
const CONFIRM: Record<OrderSide, Key> = { buy: "ticket.confirm.buy", sell: "ticket.confirm.sell" };
const PLACED: Record<OrderSide, Key> = { buy: "ticket.placed.buy", sell: "ticket.placed.sell" };

export function OrderTicket({
  instrument,
  accounts,
  limitPrice,
  onLimitPriceChange,
}: {
  instrument: Instrument | null;
  /** Already filtered to the ones that can trade this instrument. */
  accounts: Account[];
  /** Lifted so a click in the order book can fill it. */
  limitPrice: string;
  onLimitPriceChange: (next: string) => void;
}) {
  const toast = useToast();
  const t = useT();
  const [accountId, setAccountId] = useState("");
  const [side, setSide] = useState<OrderSide>("buy");
  const [orderType, setOrderType] = useState<OrderType>("limit");
  const [quantity, setQuantity] = useState("");
  const [timeInForce, setTimeInForce] = useState("GTC");
  const [reduceOnly, setReduceOnly] = useState(false);
  const [sealed, setSealed] = useState<SealedPreview | null>(null);

  const preview = usePreviewOrder();
  const place = usePlaceOrder();

  // Pick the only account, or the first, rather than making the operator choose
  // between one option. Re-runs when the instrument changes the candidate set.
  useEffect(() => {
    if (accounts.some((account) => account.accountId === accountId)) return;
    setAccountId(accounts[0]?.accountId ?? "");
  }, [accounts, accountId]);

  // Any edit invalidates the seal: it was priced against the previous values,
  // and confirming it afterwards would send something the operator never saw.
  useEffect(() => {
    setSealed(null);
  }, [instrument?.instrumentId, accountId, side, orderType, quantity, limitPrice, timeInForce, reduceOnly]);

  const remaining = useSealCountdown(sealed);
  const expired = sealed !== null && remaining <= 0;

  const quantityError = quantity !== "" && !isPositiveDecimal(quantity) ? t("ticket.positive") : null;
  const priceError =
    orderType === "limit" && limitPrice !== "" && !isPositiveDecimal(limitPrice) ? t("ticket.positive") : null;

  const ready =
    instrument !== null &&
    accountId !== "" &&
    isPositiveDecimal(quantity) &&
    (orderType === "market" || isPositiveDecimal(limitPrice));

  const onReview = () => {
    if (!instrument || !ready) return;
    preview.mutate(
      {
        accountId,
        instrumentId: instrument.instrumentId,
        side,
        orderType,
        quantity,
        // A market order must carry no price at all; the server rejects one
        // that does, rather than ignoring it.
        limitPrice: orderType === "limit" ? limitPrice : "",
        timeInForce,
        reduceOnly,
      },
      {
        onSuccess: setSealed,
        onError: (error) =>
          toast.push({ tone: "danger", title: t("ticket.previewRejected"), detail: describeError(error, t) }),
      },
    );
  };

  const onConfirm = () => {
    if (!sealed || expired) return;
    place.mutate(sealed.preview.previewId, {
      onSuccess: (command) => {
        setSealed(null);
        setQuantity("");
        toast.push({
          tone: "ok",
          // `command.state` is the server's own word and is not translated: it is
          // what the blotter row and the audit log call the same thing.
          title: t(PLACED[side], { state: command.state }),
          detail: command.commandId,
        });
      },
      onError: (error) => {
        // The seal is spent either way — a rejected command does not hand it
        // back — so clearing it forces a fresh price rather than a retry of a
        // token the server has already seen.
        setSealed(null);
        toast.push({ tone: "danger", title: t("ticket.orderRejected"), detail: describeError(error, t) });
      },
    });
  };

  return (
    <Panel title={t("ticket.title")} className="flex min-h-0 flex-col">
      <div className="flex flex-col gap-3">
        {/* Options are rebuilt per render rather than memoised: `useT` returns a
            fresh closure every time, so a `[t]` dependency would never hit. */}
        <Segmented
          name="side"
          label={t("ticket.side")}
          value={side}
          options={SIDES.map((entry) => ({ value: entry.value, label: t(entry.labelKey), tone: entry.tone }))}
          onChange={setSide}
        />

        <Field label={t("ticket.account")} required>
          {(field) => (
            <select
              {...field}
              className={inputClass}
              value={accountId}
              onChange={(event) => setAccountId(event.target.value)}
              disabled={accounts.length === 0}
            >
              {accounts.length === 0 ? <option value="">{t("ticket.noAccount")}</option> : null}
              {accounts.map((account) => (
                <option key={account.accountId} value={account.accountId}>
                  {account.displayName} · {account.venue}
                </option>
              ))}
            </select>
          )}
        </Field>

        <Segmented
          name="order-type"
          label={t("ticket.orderType")}
          value={orderType}
          options={ORDER_TYPES.map((entry) => ({ value: entry.value, label: t(entry.labelKey) }))}
          onChange={(next) => {
            setOrderType(next);
            // A resting market order is not a thing. Immediate-or-cancel is the
            // only honest time-in-force for one, so it is set and locked.
            setTimeInForce(next === "market" ? "IOC" : "GTC");
          }}
        />

        <Field
          label={`${t("ticket.price")}${instrument?.quoteAsset ? ` (${instrument.quoteAsset})` : ""}`}
          error={priceError}
          hint={orderType === "market" ? t("ticket.price.market") : t("ticket.price.hint")}
          required={orderType === "limit"}
        >
          {(field) => (
            <Input
              {...field}
              inputMode="decimal"
              autoComplete="off"
              value={orderType === "market" ? "" : limitPrice}
              placeholder={orderType === "market" ? "—" : "0.00"}
              disabled={orderType === "market"}
              onChange={(event) => onLimitPriceChange(event.target.value)}
            />
          )}
        </Field>

        <Field
          label={`${t("ticket.quantity")}${instrument?.baseAsset ? ` (${instrument.baseAsset})` : ""}`}
          error={quantityError}
          required
        >
          {(field) => (
            <Input
              {...field}
              inputMode="decimal"
              autoComplete="off"
              value={quantity}
              placeholder="0.00"
              onChange={(event) => setQuantity(event.target.value)}
            />
          )}
        </Field>

        <div className="flex items-end gap-3">
          <Field label={t("ticket.tif")} className="flex-1">
            {(field) => (
              <select
                {...field}
                className={inputClass}
                value={timeInForce}
                onChange={(event) => setTimeInForce(event.target.value)}
                disabled={orderType === "market"}
              >
                <option value="GTC">GTC</option>
                <option value="IOC">IOC</option>
                <option value="FOK">FOK</option>
              </select>
            )}
          </Field>

          <label className="flex h-[30px] items-center gap-2 text-base text-muted">
            <input
              type="checkbox"
              checked={reduceOnly}
              onChange={(event) => setReduceOnly(event.target.checked)}
              className="size-3.5 accent-[var(--bt-accent)]"
            />
            {t("ticket.reduceOnly")}
          </label>
        </div>

        <Notional quantity={quantity} price={limitPrice} asset={instrument?.quoteAsset} active={orderType === "limit"} />

        {sealed === null ? (
          <Button
            variant="primary"
            size="lg"
            onClick={onReview}
            disabled={!ready || preview.isPending}
            className="w-full"
          >
            {preview.isPending ? t("ticket.pricing") : t("ticket.review")}
          </Button>
        ) : (
          <SealedConfirmation
            sealed={sealed}
            side={side}
            remaining={remaining}
            expired={expired}
            placing={place.isPending}
            onConfirm={onConfirm}
            onDiscard={() => setSealed(null)}
          />
        )}
      </div>
    </Panel>
  );
}

/* --- The sealed half ----------------------------------------------------- */

function SealedConfirmation({
  sealed,
  side,
  remaining,
  expired,
  placing,
  onConfirm,
  onDiscard,
}: {
  sealed: SealedPreview;
  side: OrderSide;
  remaining: number;
  expired: boolean;
  placing: boolean;
  onConfirm: () => void;
  onDiscard: () => void;
}) {
  const { blockers, warnings } = sealed.preview;
  const blocked = blockers.length > 0;
  const t = useT();

  return (
    // A well, not a card: §5 gives inner containers no border, so the seal
    // separates from the form by fill and space. It still has to read as the
    // one thing on the ticket that is now live, which is what the `Confirm`
    // header and the countdown beside it are for.
    <div className="flex flex-col gap-2 rounded-card bg-fill p-2.5">
      <div className="flex items-center justify-between">
        <span className="th">{t("ticket.confirm")}</span>
        <span className={expired ? "text-meta text-danger" : "text-meta text-dim"} role="timer" aria-live="off">
          {expired ? t("ticket.expired") : t("ticket.left", { duration: formatDuration(remaining) })}
        </span>
      </div>

      {blocked ? (
        <ul className="flex flex-col gap-1 text-meta text-danger">
          {blockers.map((blocker) => (
            <li key={blocker}>{blocker}</li>
          ))}
        </ul>
      ) : null}

      {warnings.length > 0 ? (
        <ul className="flex flex-col gap-1 text-meta text-warn">
          {warnings.map((warning) => (
            <li key={warning}>{warning}</li>
          ))}
        </ul>
      ) : null}

      <div className="flex gap-2">
        <Button variant="ghost" size="lg" onClick={onDiscard} className="shrink-0">
          {t("ticket.discard")}
        </Button>
        <Button
          variant="primary"
          size="lg"
          onClick={onConfirm}
          disabled={blocked || expired || placing}
          className="flex-1"
        >
          {placing ? t("ticket.placing") : expired ? t("ticket.expiredButton") : t(CONFIRM[side])}
        </Button>
      </div>
    </div>
  );
}

/** Ticks the seal's remaining lifetime. Stops itself once it reaches zero. */
function useSealCountdown(sealed: SealedPreview | null): number {
  const [remaining, setRemaining] = useState(MANUAL_PREVIEW_TTL_MS);

  useEffect(() => {
    if (!sealed) {
      setRemaining(MANUAL_PREVIEW_TTL_MS);
      return;
    }
    const tick = () => {
      const left = sealed.sealedAt + MANUAL_PREVIEW_TTL_MS - Date.now();
      setRemaining(left);
      return left;
    };
    if (tick() <= 0) return;
    const timer = window.setInterval(() => {
      if (tick() <= 0) window.clearInterval(timer);
    }, 250);
    return () => window.clearInterval(timer);
  }, [sealed]);

  return remaining;
}

/* --- Helpers ------------------------------------------------------------- */

function isPositiveDecimal(value: string): boolean {
  const parts = splitDecimal(value);
  if (!parts || parts.sign === "-") return false;
  return !/^0*$/.test(parts.integer) || !/^0*$/.test(parts.fraction);
}

/**
 * Quantity × price, for eyeballing the size before confirming.
 *
 * Float again, and display-only again — the strings above are what get sent.
 * It used to earn its place by showing a notional cap coming before it landed as
 * a blocker. There is no cap now, and no test venue underneath it either, so it
 * earns its place for the opposite reason: this is the only line on the ticket
 * that says how much real money the two fields above it add up to.
 */
function Notional({
  quantity,
  price,
  asset,
  active,
}: {
  quantity: string;
  price: string;
  asset?: string;
  active: boolean;
}) {
  // Before the early returns, because a hook cannot sit behind a branch.
  const t = useT();
  if (!active || !isPositiveDecimal(quantity) || !isPositiveDecimal(price)) {
    return <div className="h-4" />;
  }
  const value = Number.parseFloat(quantity) * Number.parseFloat(price);
  if (!Number.isFinite(value)) return <div className="h-4" />;
  return (
    <div className="flex h-4 items-center justify-between text-meta text-dim">
      <span>{t("ticket.notional")}</span>
      <span data-numeric="">
        {formatDecimal(value.toFixed(2))} {asset ?? ""}
      </span>
    </div>
  );
}

