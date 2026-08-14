import { useEffect, useState } from "react";

import { describeError } from "@/api/errors";
import type { Account, Instrument, Mode, OrderSide, OrderType } from "@/api/types";
import { MANUAL_PREVIEW_TTL_MS } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Panel } from "@/components/ui/panel";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { usePlaceOrder, usePreviewOrder, type SealedPreview } from "@/features/trade/queries";
import { formatDecimal, formatDuration, splitDecimal } from "@/lib/format";

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

const SIDES: { value: OrderSide; label: string; tone: "up" | "down" }[] = [
  { value: "buy", label: "Buy", tone: "up" },
  { value: "sell", label: "Sell", tone: "down" },
];

const ORDER_TYPES: { value: OrderType; label: string }[] = [
  { value: "limit", label: "Limit" },
  { value: "market", label: "Market" },
];

export function OrderTicket({
  instrument,
  accounts,
  mode,
  limitPrice,
  onLimitPriceChange,
}: {
  instrument: Instrument | null;
  /** Already filtered to the ones that can trade this instrument in this mode. */
  accounts: Account[];
  mode: Mode;
  /** Lifted so a click in the order book can fill it. */
  limitPrice: string;
  onLimitPriceChange: (next: string) => void;
}) {
  const toast = useToast();
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

  const quantityError = quantity !== "" && !isPositiveDecimal(quantity) ? "Must be a positive number." : null;
  const priceError =
    orderType === "limit" && limitPrice !== "" && !isPositiveDecimal(limitPrice)
      ? "Must be a positive number."
      : null;

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
        mode,
      },
      {
        onSuccess: setSealed,
        onError: (error) => toast.push({ tone: "danger", title: "Preview rejected", detail: describeError(error) }),
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
          title: `${side === "buy" ? "Buy" : "Sell"} order ${command.state}`,
          detail: command.commandId,
        });
      },
      onError: (error) => {
        // The seal is spent either way — a rejected command does not hand it
        // back — so clearing it forces a fresh price rather than a retry of a
        // token the server has already seen.
        setSealed(null);
        toast.push({ tone: "danger", title: "Order rejected", detail: describeError(error) });
      },
    });
  };

  return (
    <Panel title="Order" meta={<span className="uppercase">{mode}</span>} className="flex min-h-0 flex-col">
      <div className="flex flex-col gap-3">
        <Segmented name="side" label="Side" value={side} options={SIDES} onChange={setSide} />

        <Field label="Account" required>
          {(field) => (
            <select
              {...field}
              className={inputClass}
              value={accountId}
              onChange={(event) => setAccountId(event.target.value)}
              disabled={accounts.length === 0}
            >
              {accounts.length === 0 ? <option value="">No account for this venue and mode</option> : null}
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
          label="Order type"
          value={orderType}
          options={ORDER_TYPES}
          onChange={(next) => {
            setOrderType(next);
            // A resting market order is not a thing. Immediate-or-cancel is the
            // only honest time-in-force for one, so it is set and locked.
            setTimeInForce(next === "market" ? "IOC" : "GTC");
          }}
        />

        <Field
          label={`Price${instrument?.quoteAsset ? ` (${instrument.quoteAsset})` : ""}`}
          error={priceError}
          hint={orderType === "market" ? "Market orders take the book price." : "Click a level in the book to fill this."}
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
          label={`Quantity${instrument?.baseAsset ? ` (${instrument.baseAsset})` : ""}`}
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
          <Field label="Time in force" className="flex-1">
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
            Reduce only
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
            {preview.isPending ? "Pricing…" : "Review order"}
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

  return (
    <div className="flex flex-col gap-2 rounded-md border border-line-strong p-2.5">
      <div className="flex items-center justify-between">
        <span className="th">Confirm</span>
        <span className={expired ? "text-meta text-danger" : "text-meta text-dim"} role="timer" aria-live="off">
          {expired ? "expired" : `${formatDuration(remaining)} left`}
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
          Discard
        </Button>
        <Button
          variant="primary"
          size="lg"
          onClick={onConfirm}
          disabled={blocked || expired || placing}
          className="flex-1"
        >
          {placing ? "Placing…" : expired ? "Preview expired" : `Confirm ${side}`}
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
 * It earns its place because test mode caps notional at 100 and an operator
 * should be able to see the cap coming rather than discover it in a blocker.
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
  if (!active || !isPositiveDecimal(quantity) || !isPositiveDecimal(price)) {
    return <div className="h-4" />;
  }
  const value = Number.parseFloat(quantity) * Number.parseFloat(price);
  if (!Number.isFinite(value)) return <div className="h-4" />;
  return (
    <div className="flex h-4 items-center justify-between text-meta text-dim">
      <span>Order value</span>
      <span data-numeric="">
        {formatDecimal(value.toFixed(2))} {asset ?? ""}
      </span>
    </div>
  );
}

