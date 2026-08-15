import { ArrowUpDown } from "lucide-react";
import { useEffect, useState } from "react";

import { describeError } from "@/api/errors";
import { ARBITRAGE_PREVIEW_TTL_MS } from "@/api/types";
import type { Account, ArbitrageOpportunityLeg, Mode, OrderType } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Num, Side } from "@/components/ui/num";
import { Panel } from "@/components/ui/panel";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import {
  useOpenArbitrage,
  usePreviewArbitrage,
  type SealedArbitragePreview,
} from "@/features/funding/queries";
import { formatDuration, splitDecimal } from "@/lib/format";

/* ---------------------------------------------------------------------------
 * The hedge ticket.
 *
 * Same two steps as the order ticket — a preview seals the priced intent, a
 * command spends the seal — with one difference that matters: the seal carries
 * *both* legs' authority, so anything that changes either leg invalidates the
 * whole hedge rather than half of it. This file's job is not to let an operator
 * confirm something they did not see, which is why every edit below voids it.
 *
 * The swap is not cosmetic. Which venue is the long leg decides the sign of the
 * spread, and the server says so rather than refusing: it returns
 * `negative_spread` as a warning, not a blocker, because paying funding to hold
 * a hedge is a choice an operator is allowed to make.
 * ------------------------------------------------------------------------- */

const ORDER_TYPES: { value: OrderType; label: string }[] = [
  { value: "limit", label: "Limit" },
  { value: "market", label: "Market" },
];

export function ArbitrageTicket({
  symbol,
  baseAsset,
  quoteAsset,
  long,
  short,
  accounts,
  mode,
  onSwap,
}: {
  symbol: string;
  baseAsset: string;
  quoteAsset: string;
  long: ArbitrageOpportunityLeg;
  short: ArbitrageOpportunityLeg;
  /** Arbitrage-capable accounts for this mode, both venues mixed. */
  accounts: Account[];
  mode: Mode;
  onSwap: () => void;
}) {
  const toast = useToast();
  const [longChoice, setLongChoice] = useState<string | null>(null);
  const [shortChoice, setShortChoice] = useState<string | null>(null);
  const [quantity, setQuantity] = useState("");
  const [orderType, setOrderType] = useState<OrderType>("market");
  const [longLimit, setLongLimit] = useState("");
  const [shortLimit, setShortLimit] = useState("");
  const [timeInForce, setTimeInForce] = useState("IOC");
  const [sealed, setSealed] = useState<SealedArbitragePreview | null>(null);

  const preview = usePreviewArbitrage();
  const open = useOpenArbitrage();

  const longAccounts = accounts.filter((account) => account.venue === long.venue);
  const shortAccounts = accounts.filter((account) => account.venue === short.venue);

  // Derived rather than stored: the candidate list changes when the pair does,
  // and a stored id would survive a swap that left it pointing at the wrong
  // venue — which the server would reject only after the operator had asked.
  const longAccountId = pickAccount(longChoice, long.accountId, longAccounts);
  const shortAccountId = pickAccount(shortChoice, short.accountId, shortAccounts);

  // Any edit voids the seal — including the swap, which changes which venue the
  // sealed authority points each side at.
  useEffect(() => {
    setSealed(null);
  }, [
    long.venue,
    long.instrumentId,
    short.venue,
    short.instrumentId,
    longAccountId,
    shortAccountId,
    quantity,
    orderType,
    longLimit,
    shortLimit,
    timeInForce,
  ]);

  const remaining = useSealCountdown(sealed);
  const expired = sealed !== null && remaining <= 0;

  const quantityError = quantity !== "" && !isPositiveDecimal(quantity) ? "Must be a positive number." : null;
  const reachable = long.instrumentId !== undefined && short.instrumentId !== undefined;
  const ready =
    reachable &&
    longAccountId !== "" &&
    shortAccountId !== "" &&
    // The server rejects a hedge whose legs share an account: that is the same
    // position opened twice, not a hedge.
    longAccountId !== shortAccountId &&
    isPositiveDecimal(quantity) &&
    (orderType === "market" || (isPositiveDecimal(longLimit) && isPositiveDecimal(shortLimit)));

  const edge = spreadOf(long.dailyRate, short.dailyRate);

  const onReview = () => {
    if (!ready || long.instrumentId === undefined || short.instrumentId === undefined) return;
    preview.mutate(
      {
        long: {
          accountId: longAccountId,
          instrumentId: long.instrumentId,
          // A market leg must carry no price at all — the server rejects one
          // that does rather than ignoring it.
          ...(orderType === "limit" ? { limitPrice: longLimit } : {}),
        },
        short: {
          accountId: shortAccountId,
          instrumentId: short.instrumentId,
          ...(orderType === "limit" ? { limitPrice: shortLimit } : {}),
        },
        quantity,
        orderType,
        timeInForce,
        mode,
      },
      {
        onSuccess: setSealed,
        onError: (error) =>
          toast.push({ tone: "danger", title: "Preview rejected", detail: describeError(error) }),
      },
    );
  };

  const onConfirm = () => {
    if (!sealed || expired) return;
    open.mutate(sealed.preview.previewId, {
      onSuccess: (execution) => {
        setSealed(null);
        setQuantity("");
        const partial = execution.state === "partially_open" || execution.failures.length > 0;
        toast.push({
          tone: partial ? "danger" : "ok",
          title: partial ? "Only one leg opened" : `Hedge ${execution.state}`,
          detail: partial
            ? "The pair is carrying one-sided exposure. Dispose of the open leg below."
            : execution.lineageId,
        });
      },
      onError: (error) => {
        // The seal is spent whether or not the legs landed, so a retry has to
        // start from a fresh price rather than from a token the server has seen.
        setSealed(null);
        toast.push({ tone: "danger", title: "Hedge rejected", detail: describeError(error) });
      },
    });
  };

  return (
    <Panel title="Hedge" meta={<span className="uppercase">{mode}</span>}>
      <div className="flex flex-col gap-3">
        <LegCard
          leg="long"
          venue={long.venue}
          environment={long.environment}
          dailyRate={long.dailyRate}
          stale={long.stale ?? false}
          unlisted={long.instrumentId === undefined}
          accounts={longAccounts}
          accountId={longAccountId}
          onAccountChange={setLongChoice}
          limitPrice={longLimit}
          onLimitPriceChange={setLongLimit}
          showLimit={orderType === "limit"}
          quoteAsset={quoteAsset}
        />

        <div className="flex items-center gap-2">
          <span aria-hidden className="h-px flex-1 bg-line" />
          <Button variant="outline" size="sm" onClick={onSwap} aria-label="Swap which venue is the long leg">
            <ArrowUpDown aria-hidden className="size-3" />
            Swap
          </Button>
          <span aria-hidden className="h-px flex-1 bg-line" />
        </div>

        <LegCard
          leg="short"
          venue={short.venue}
          environment={short.environment}
          dailyRate={short.dailyRate}
          stale={short.stale ?? false}
          unlisted={short.instrumentId === undefined}
          accounts={shortAccounts}
          accountId={shortAccountId}
          onAccountChange={setShortChoice}
          limitPrice={shortLimit}
          onLimitPriceChange={setShortLimit}
          showLimit={orderType === "limit"}
          quoteAsset={quoteAsset}
        />

        <Segmented
          name="arbitrage-order-type"
          label="Order type"
          value={orderType}
          options={ORDER_TYPES}
          onChange={(next) => {
            setOrderType(next);
            // Both legs go out together and a resting hedge is half a hedge for
            // as long as it rests, so a market pair is immediate-or-cancel.
            setTimeInForce(next === "market" ? "IOC" : "GTC");
          }}
        />

        <Field label={`Quantity (${baseAsset || "base"})`} error={quantityError} hint="Same size both legs." required>
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

        <Edge
          symbol={symbol}
          quoteAsset={quoteAsset}
          spreadDaily={edge}
          quantity={quantity}
          longMark={long.markPrice}
          shortMark={short.markPrice}
        />

        {!reachable ? (
          <p className="text-meta text-warn" role="status">
            One of these venues has no reachable account in {mode} mode, so this pair cannot be priced.
          </p>
        ) : null}

        {sealed === null ? (
          <Button
            variant="primary"
            size="lg"
            onClick={onReview}
            disabled={!ready || preview.isPending}
            className="w-full"
          >
            {preview.isPending ? "Pricing…" : "Review hedge"}
          </Button>
        ) : (
          <SealedConfirmation
            sealed={sealed}
            remaining={remaining}
            expired={expired}
            opening={open.isPending}
            onConfirm={onConfirm}
            onDiscard={() => setSealed(null)}
          />
        )}
      </div>
    </Panel>
  );
}

/* --- One leg ------------------------------------------------------------- */

function LegCard({
  leg,
  venue,
  environment,
  dailyRate,
  stale,
  unlisted,
  accounts,
  accountId,
  onAccountChange,
  limitPrice,
  onLimitPriceChange,
  showLimit,
  quoteAsset,
}: {
  leg: "long" | "short";
  venue: string;
  environment: string;
  dailyRate: string | undefined;
  stale: boolean;
  unlisted: boolean;
  accounts: Account[];
  accountId: string;
  onAccountChange: (next: string) => void;
  limitPrice: string;
  onLimitPriceChange: (next: string) => void;
  showLimit: boolean;
  quoteAsset: string;
}) {
  const percent = percentOf(dailyRate);
  const priceError = showLimit && limitPrice !== "" && !isPositiveDecimal(limitPrice) ? "Must be positive." : null;

  return (
    <div className="flex flex-col gap-2 rounded-md border border-line p-2.5">
      <div className="flex items-baseline justify-between gap-2">
        <div className="flex min-w-0 items-baseline gap-2">
          <Side side={leg} />
          <span className="truncate text-base text-text">{venue}</span>
          <span className="shrink-0 text-meta text-dim">{environment}</span>
        </div>
        <div className="flex shrink-0 items-baseline gap-1">
          {percent === null ? (
            <span className="text-meta text-dim">no rate</span>
          ) : (
            <Num value={percent} decimals={4} signed directional className="text-base" />
          )}
          <span className="text-meta text-dim">%/d</span>
        </div>
      </div>

      {stale ? <p className="text-meta text-warn">This venue&rsquo;s quote is stale.</p> : null}
      {unlisted ? <p className="text-meta text-warn">No instrument on this venue for this account.</p> : null}

      <Field label="Account" required>
        {(field) => (
          <select
            {...field}
            className={inputClass}
            value={accountId}
            onChange={(event) => onAccountChange(event.target.value)}
            disabled={accounts.length === 0}
          >
            {accounts.length === 0 ? <option value="">No account for {venue}</option> : null}
            {accounts.map((account) => (
              <option key={account.accountId} value={account.accountId}>
                {account.displayName}
              </option>
            ))}
          </select>
        )}
      </Field>

      {showLimit ? (
        <Field label={`Limit price (${quoteAsset || "quote"})`} error={priceError} required>
          {(field) => (
            <Input
              {...field}
              inputMode="decimal"
              autoComplete="off"
              value={limitPrice}
              placeholder="0.00"
              onChange={(event) => onLimitPriceChange(event.target.value)}
            />
          )}
        </Field>
      ) : null}
    </div>
  );
}

/* --- What the pair earns ------------------------------------------------- */

/**
 * The spread and what it is worth per day.
 *
 * The arithmetic here is the server's own — `short − long`, annualised at 365 —
 * repeated locally so the figures stay coherent through a swap the server has
 * not been asked about yet. The sealed preview replaces them with the server's
 * before anything is confirmed.
 *
 * Float, and display-only: the strings above are what get sent. It earns its
 * place because a hedge is opened for this number and nothing else, and an
 * operator should see it before asking for a price rather than after.
 */
function Edge({
  symbol,
  quoteAsset,
  spreadDaily,
  quantity,
  longMark,
  shortMark,
}: {
  symbol: string;
  quoteAsset: string;
  spreadDaily: number | null;
  quantity: string;
  longMark: string | undefined;
  shortMark: string | undefined;
}) {
  if (spreadDaily === null) {
    return (
      <p className="text-meta text-dim">
        {symbol}: no comparable daily rate — one venue publishes no funding interval.
      </p>
    );
  }
  const notional = notionalOf(quantity, longMark, shortMark);
  return (
    <dl className="flex flex-col gap-1 text-meta">
      <div className="flex items-baseline justify-between">
        <dt className="text-dim">Spread</dt>
        <dd>
          <Num value={(spreadDaily * 100).toFixed(4)} decimals={4} signed directional />
          <span className="text-dim"> %/d</span>
        </dd>
      </div>
      <div className="flex items-baseline justify-between">
        <dt className="text-dim">APR</dt>
        <dd>
          <Num value={(spreadDaily * 365 * 100).toFixed(2)} decimals={2} signed directional />
          <span className="text-dim"> %</span>
        </dd>
      </div>
      <div className="flex items-baseline justify-between">
        <dt className="text-dim">Funding per day</dt>
        <dd>
          {notional === null ? (
            <span className="text-dim">—</span>
          ) : (
            <>
              <Num value={(notional * spreadDaily).toFixed(4)} decimals={4} signed directional />
              <span className="text-dim"> {quoteAsset}</span>
            </>
          )}
        </dd>
      </div>
      <p className="text-dim">Gross — venue fees are priced per route at execution.</p>
    </dl>
  );
}

/* --- The sealed half ----------------------------------------------------- */

function SealedConfirmation({
  sealed,
  remaining,
  expired,
  opening,
  onConfirm,
  onDiscard,
}: {
  sealed: SealedArbitragePreview;
  remaining: number;
  expired: boolean;
  opening: boolean;
  onConfirm: () => void;
  onDiscard: () => void;
}) {
  const { blockers, warnings, spreadDaily, spreadApr, notional, dailyFunding } = sealed.preview;
  const blocked = blockers.length > 0;

  return (
    <div className="flex flex-col gap-2 rounded-md border border-line-strong p-2.5">
      <div className="flex items-center justify-between">
        <span className="th">Confirm hedge</span>
        <span className={expired ? "text-meta text-danger" : "text-meta text-dim"} role="timer" aria-live="off">
          {expired ? "expired" : `${formatDuration(remaining)} left`}
        </span>
      </div>

      <dl className="flex flex-col gap-1 text-meta">
        <div className="flex items-baseline justify-between">
          <dt className="text-dim">Sealed spread</dt>
          <dd>
            <Num value={spreadDaily} decimals={6} signed directional />
            <span className="text-dim"> /d</span>
          </dd>
        </div>
        <div className="flex items-baseline justify-between">
          <dt className="text-dim">Sealed APR</dt>
          <dd>
            <Num value={spreadApr} decimals={4} signed directional />
          </dd>
        </div>
        <div className="flex items-baseline justify-between">
          <dt className="text-dim">Notional</dt>
          <dd>
            <Num value={notional} decimals={2} grouped />
          </dd>
        </div>
        <div className="flex items-baseline justify-between">
          <dt className="text-dim">Funding per day</dt>
          <dd>
            <Num value={dailyFunding} decimals={4} signed directional />
          </dd>
        </div>
      </dl>

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
          disabled={blocked || expired || opening}
          className="flex-1"
        >
          {opening ? "Opening…" : expired ? "Preview expired" : "Open both legs"}
        </Button>
      </div>
    </div>
  );
}

/** Ticks the seal's remaining lifetime. Stops itself once it reaches zero. */
function useSealCountdown(sealed: SealedArbitragePreview | null): number {
  const [remaining, setRemaining] = useState(ARBITRAGE_PREVIEW_TTL_MS);

  useEffect(() => {
    if (!sealed) {
      setRemaining(ARBITRAGE_PREVIEW_TTL_MS);
      return;
    }
    const tick = () => {
      const left = sealed.sealedAt + ARBITRAGE_PREVIEW_TTL_MS - Date.now();
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

/**
 * The operator's pick, the scan's suggestion, or the first reachable account —
 * in that order, and only ever one that is on this leg's own list.
 */
function pickAccount(choice: string | null, suggested: string | undefined, candidates: Account[]): string {
  const listed = (id: string | null | undefined): id is string =>
    typeof id === "string" && candidates.some((account) => account.accountId === id);
  if (listed(choice)) return choice;
  if (listed(suggested)) return suggested;
  return candidates[0]?.accountId ?? "";
}

function isPositiveDecimal(value: string): boolean {
  const parts = splitDecimal(value);
  if (!parts || parts.sign === "-") return false;
  return !/^0*$/.test(parts.integer) || !/^0*$/.test(parts.fraction);
}

/** `short − long`, as a fraction per day. Null when either leg has no daily rate. */
export function spreadOf(longDaily: string | undefined, shortDaily: string | undefined): number | null {
  const long = Number.parseFloat(longDaily ?? "");
  const short = Number.parseFloat(shortDaily ?? "");
  if (!Number.isFinite(long) || !Number.isFinite(short)) return null;
  return short - long;
}

/** A fractional rate as a percent string, ready for `Num`. */
function percentOf(rate: string | undefined): string | null {
  const parsed = Number.parseFloat(rate ?? "");
  return Number.isFinite(parsed) ? (parsed * 100).toFixed(4) : null;
}

/** Quantity × the mean of the two marks, the same average the server takes. */
function notionalOf(quantity: string, longMark: string | undefined, shortMark: string | undefined): number | null {
  if (!isPositiveDecimal(quantity)) return null;
  const marks = [longMark, shortMark]
    .map((mark) => Number.parseFloat(mark ?? ""))
    .filter((mark) => Number.isFinite(mark) && mark > 0);
  if (marks.length === 0) return null;
  const average = marks.reduce((sum, mark) => sum + mark, 0) / marks.length;
  const value = Number.parseFloat(quantity) * average;
  return Number.isFinite(value) ? value : null;
}
