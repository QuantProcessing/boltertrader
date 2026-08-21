import { ArrowUpDown } from "lucide-react";
import { useEffect, useState } from "react";

import { describeError } from "@/api/errors";
import { ARBITRAGE_PREVIEW_TTL_MS } from "@/api/types";
import type { Account, AccountBalance, ArbitrageOpportunityLeg, ArbitrageStrategy, OrderType } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Num } from "@/components/ui/num";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { VenueBadge } from "@/components/ui/venue-badge";
import {
  useOpenArbitrage,
  usePreviewArbitrage,
  type SealedArbitragePreview,
} from "@/features/funding/queries";
import { useAccountBalances } from "@/features/trade/queries";
import { formatDuration, splitDecimal } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const QUANTITY_PERCENTAGES = [25, 50, 75, 100] as const;

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

export function ArbitrageTicket({
	strategy,
  symbol,
  baseAsset,
  quoteAsset,
  long,
  short,
  accounts,
  onSwap,
}: {
	strategy: ArbitrageStrategy;
  symbol: string;
  baseAsset: string;
  quoteAsset: string;
  long: ArbitrageOpportunityLeg;
  short: ArbitrageOpportunityLeg;
  /** Arbitrage-capable accounts, both venues mixed. */
  accounts: Account[];
  onSwap: () => void;
}) {
  const t = useT();
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

  const longAccounts = accounts.filter((account) => accountSupportsLeg(account, long));
  const shortAccounts = accounts.filter((account) => accountSupportsLeg(account, short));

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

  const quantityError = quantity !== "" && !isPositiveDecimal(quantity) ? t("ticket.positive") : null;
  const reachable = long.instrumentId !== undefined && short.instrumentId !== undefined;
  const ready =
    reachable &&
    longAccountId !== "" &&
    shortAccountId !== "" &&
    isPositiveDecimal(quantity) &&
    (orderType === "market" || (isPositiveDecimal(longLimit) && isPositiveDecimal(shortLimit)));

  const edge = strategy === "cross_perp_funding"
    ? spreadOf(long.dailyRate, short.dailyRate)
    : priceEdgeOf(long.referencePrice, short.referencePrice, strategy);
  const longBalances = useAccountBalances(longAccountId);
  const shortBalances = useAccountBalances(shortAccountId);
  const longAvailable = availableMargin(longBalances.data, quoteAsset);
  const shortAvailable = availableMargin(shortBalances.data, quoteAsset);
  const maxQuantity = hedgeCapacity(longAvailable, long.referencePrice, shortAvailable, short.referencePrice);
  const selectedQuantityPercent = maxQuantity === null
    ? null
    : QUANTITY_PERCENTAGES.find((percent) => quantityAtPercent(maxQuantity, percent) === quantity) ?? null;

  // Built here rather than at module scope: a module-level array is evaluated
  // once at import and would freeze whichever locale was active then. Inline
  // rather than memoised, because `useT()` returns a fresh closure every render
  // and a `useMemo` keyed on it would never hit.
  const orderTypes: { value: OrderType; label: string }[] = [
    { value: "limit", label: t("ticket.type.limit") },
    { value: "market", label: t("ticket.type.market") },
  ];

  const onReview = () => {
    if (!ready || long.instrumentId === undefined || short.instrumentId === undefined) return;
    preview.mutate(
      {
		strategy,
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
    open.mutate(sealed.preview.previewId, {
      onSuccess: (execution) => {
        setSealed(null);
        setQuantity("");
        const partial = execution.state === "partially_open" || execution.failures.length > 0;
        toast.push({
          tone: partial ? "danger" : "ok",
          // `execution.state` is the server's own word, as on the order ticket.
          title: partial ? t("arb.partial") : t("arb.opened", { state: execution.state }),
          detail: partial ? t("arb.partial.detail") : execution.lineageId,
        });
      },
      onError: (error) => {
        // The seal is spent whether or not the legs landed, so a retry has to
        // start from a fresh price rather than from a token the server has seen.
        setSealed(null);
        toast.push({ tone: "danger", title: t("arb.rejected"), detail: describeError(error, t) });
      },
    });
  };

  return (
    <section className="min-h-full bg-panel">
      <header className="flex h-row items-center justify-between border-b border-line px-3">
        <h2 className="text-title font-semibold text-text">{strategy === "spot_perp_basis" ? t("arb.executeOrder") : t("arb.executeHedge")}</h2>
        <span className="text-meta text-dim">{symbol}</span>
      </header>
      <div className="flex flex-col">
        <div className="flex items-center justify-between border-b border-line px-3 py-2 text-meta">
          <span className="text-dim">{t("arb.direction")}</span>
          <span className="text-right">
            <span className="text-up">{long.product === "spot" ? t("arb.buySpot") : t("arb.long")}</span>
            <span className="px-1.5 text-dim">/</span>
            <span className="text-down">{t("arb.sellPerp")}</span>
          </span>
        </div>
        <LegCard
          leg="long"
          venue={long.venue}
          dailyRate={long.dailyRate}
		  product={long.product}
		  referencePrice={long.referencePrice}
          stale={long.stale ?? false}
          unlisted={long.instrumentId === undefined}
          accounts={longAccounts}
          accountId={longAccountId}
          onAccountChange={setLongChoice}
          limitPrice={longLimit}
          onLimitPriceChange={setLongLimit}
          showLimit={orderType === "limit"}
          quoteAsset={quoteAsset}
          baseAsset={baseAsset}
          availableMargin={longAvailable}
        />

		{strategy === "spot_perp_basis" ? null : <div className="flex items-center gap-2 px-3 py-1.5">
          <span aria-hidden className="h-px flex-1 bg-line" />
          <Button variant="outline" size="sm" onClick={onSwap} aria-label={t("arb.swap.label")}>
            <ArrowUpDown aria-hidden className="size-3" />
            {t("arb.swap")}
          </Button>
          <span aria-hidden className="h-px flex-1 bg-line" />
		</div>}

        <LegCard
          leg="short"
          venue={short.venue}
          dailyRate={short.dailyRate}
		  product={short.product}
		  referencePrice={short.referencePrice}
          stale={short.stale ?? false}
          unlisted={short.instrumentId === undefined}
          accounts={shortAccounts}
          accountId={shortAccountId}
          onAccountChange={setShortChoice}
          limitPrice={shortLimit}
          onLimitPriceChange={setShortLimit}
          showLimit={orderType === "limit"}
          quoteAsset={quoteAsset}
          baseAsset={baseAsset}
          availableMargin={shortAvailable}
        />

        <div className="flex flex-col gap-3 border-t border-line px-3 py-3">
          <Segmented
            name="arbitrage-order-type"
            label={t("ticket.orderType")}
            value={orderType}
            options={orderTypes}
            onChange={(next) => {
              setOrderType(next);
              setTimeInForce(next === "market" ? "IOC" : "GTC");
            }}
          />

          <Field label={t("arb.quantity", { asset: baseAsset || t("arb.baseAsset") })} error={quantityError} required>
            {(field) => (
              <div className="grid grid-cols-[minmax(96px,1fr)_minmax(184px,1.35fr)] items-center gap-2">
                <Input {...field} data-numeric="" inputMode="decimal" autoComplete="off" value={quantity} placeholder="0.00" onChange={(event) => setQuantity(event.target.value)} />
                <div className="grid h-[30px] grid-cols-4 gap-0.5 rounded-control bg-fill p-0.5 pointer-coarse:h-bar" role="group" aria-label={t("arb.quantityPercent")}>
                  {QUANTITY_PERCENTAGES.map((percent) => {
                    const selected = selectedQuantityPercent === percent;
                    return (
                      <Button
                        key={percent}
                        type="button"
                        size="sm"
                        variant="ghost"
                        aria-pressed={selected}
                        disabled={maxQuantity === null}
                        className={cn("h-[26px] min-w-0 rounded-control px-1", selected && "bg-raised text-text hover:bg-raised")}
                        onClick={() => maxQuantity !== null && setQuantity(quantityAtPercent(maxQuantity, percent))}
                      >
                        {percent}%
                      </Button>
                    );
                  })}
                </div>
              </div>
            )}
          </Field>

          <div className="border-t border-line pt-2">
            <Edge strategy={strategy} symbol={symbol} quoteAsset={quoteAsset} spreadDaily={edge} quantity={quantity} longMark={long.markPrice ?? long.referencePrice} shortMark={short.markPrice ?? short.referencePrice} longDaily={long.dailyRate} shortDaily={short.dailyRate} />
          </div>

          {!reachable ? <p className="text-meta text-warn" role="status">{t("arb.unreachable")}</p> : null}

          {sealed === null ? (
            <Button variant="primary" size="lg" onClick={onReview} disabled={!ready || preview.isPending} className="w-full bg-up text-bg hover:bg-up/90">
              {preview.isPending ? t("ticket.pricing") : t("arb.review")}
            </Button>
          ) : (
            <SealedConfirmation sealed={sealed} remaining={remaining} expired={expired} opening={open.isPending} onConfirm={onConfirm} onDiscard={() => setSealed(null)} />
          )}
        </div>
      </div>
    </section>
  );
}

/* --- One leg ------------------------------------------------------------- */

function LegCard({
  leg,
  venue,
  dailyRate,
	product,
	referencePrice,
  stale,
  unlisted,
  accounts,
  accountId,
  onAccountChange,
  limitPrice,
  onLimitPriceChange,
  showLimit,
  quoteAsset,
  baseAsset,
  availableMargin,
}: {
  leg: "long" | "short";
  venue: string;
  dailyRate: string | undefined;
	product: "spot" | "perp";
	referencePrice: string | undefined;
  stale: boolean;
  unlisted: boolean;
  accounts: Account[];
  accountId: string;
  onAccountChange: (next: string) => void;
  limitPrice: string;
  onLimitPriceChange: (next: string) => void;
  showLimit: boolean;
  quoteAsset: string;
  baseAsset: string;
  availableMargin: string | null;
}) {
  const t = useT();
  const percent = percentOf(dailyRate);
  const priceError = showLimit && limitPrice !== "" && !isPositiveDecimal(limitPrice) ? t("arb.mustBePositive") : null;
  const title = product === "spot" ? t("arb.buySpot") : leg === "long" ? t("arb.longPerp") : t("arb.sellPerp");
  const maxOpen = openCapacity(availableMargin, referencePrice);

  return (
    <div className="flex flex-col gap-2 border-b border-line px-3 py-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className={cn("text-base font-semibold", leg === "long" ? "text-up" : "text-down")}>{title}</span>
          <VenueBadge venue={venue} />
        </div>
        <div className="flex shrink-0 items-baseline gap-1">
		  {product === "spot" ? null : percent === null ? (
            <span className="text-meta text-dim">{t("arb.noRate")}</span>
          ) : (
            <Num value={percent} decimals={4} signed directional className="text-base" />
          )}
		  {product === "perp" ? <span className="text-meta text-dim">%/d</span> : null}
        </div>
      </div>
      {stale ? <p className="text-meta text-warn">{t("arb.staleQuote")}</p> : null}
      {unlisted ? <p className="text-meta text-warn">{t("arb.unlisted")}</p> : null}

      <Field label={t("ticket.account")} required>
        {(field) => (
          <select
            {...field}
            className={inputClass}
            value={accountId}
            onChange={(event) => onAccountChange(event.target.value)}
            disabled={accounts.length === 0}
          >
            {accounts.length === 0 ? <option value="">{t("arb.noAccountFor", { venue })}</option> : null}
            {accounts.map((account) => (
              <option key={account.accountId} value={account.accountId}>
                {account.displayName}
              </option>
            ))}
          </select>
        )}
      </Field>

      <div className="grid grid-cols-2 gap-3 text-meta">
        <div><p className="text-dim">{t("ticket.availableMargin")}</p><p className="mt-0.5">{availableMargin === null ? "—" : <><Num value={availableMargin} decimals={2} grouped /> {quoteAsset}</>}</p></div>
        <div><p className="text-dim">{product === "spot" ? t("arb.maxBuy") : t("arb.maxOpen")}</p><p className="mt-0.5">{maxOpen === null ? "—" : <><Num value={maxOpen.toFixed(6)} decimals={6} grouped /> {baseAsset}</>}</p></div>
      </div>

      {showLimit ? (
        <Field
          label={t("arb.limitPrice", { asset: quoteAsset || t("arb.quoteAsset") })}
          error={priceError}
          required
        >
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
	strategy,
  symbol,
  quoteAsset,
  spreadDaily,
  quantity,
  longMark,
  shortMark,
  longDaily,
  shortDaily,
}: {
	strategy: ArbitrageStrategy;
  symbol: string;
  quoteAsset: string;
  spreadDaily: number | null;
  quantity: string;
  longMark: string | undefined;
  shortMark: string | undefined;
  longDaily: string | undefined;
  shortDaily: string | undefined;
}) {
  // Above the early return: a hook cannot sit behind a conditional.
  const t = useT();

  if (spreadDaily === null) {
    return (
      <p className="text-meta text-dim">{t("arb.noComparable", { symbol })}</p>
    );
  }
  const notional = notionalOf(quantity, longMark, shortMark);
	const carryRate = strategy === "spot_perp_basis" ? numberOrNull(shortDaily) : spreadOf(longDaily, shortDaily);
	const dailyCarry = notional === null || carryRate === null ? null : notional * carryRate;
	if (strategy !== "cross_perp_funding") {
		const longNotional = legNotional(quantity, longMark);
		const shortNotional = legNotional(quantity, shortMark);
		return (
		  <dl className="flex flex-col gap-1 text-meta">
			<div className="flex items-baseline justify-between"><dt className="text-dim">{t("arb.estimatedLong")}</dt><dd>{longNotional === null ? "—" : <><Num value={longNotional.toFixed(2)} decimals={2} grouped /> <span className="text-dim">{quoteAsset}</span></>}</dd></div>
			<div className="flex items-baseline justify-between"><dt className="text-dim">{t("arb.estimatedShort")}</dt><dd>{shortNotional === null ? "—" : <><Num value={shortNotional.toFixed(2)} decimals={2} grouped /> <span className="text-dim">{quoteAsset}</span></>}</dd></div>
			<div className="flex items-baseline justify-between">
			  <dt className="text-dim">{strategy === "spot_perp_basis" ? t("arb.basis") : t("arb.priceSpread")}</dt>
			  <dd><Num value={(spreadDaily * 100).toFixed(4)} decimals={4} signed directional /><span className="text-dim"> %</span></dd>
			</div>
			<div className="flex items-baseline justify-between"><dt className="text-dim">{t("arb.fundingCarry")}</dt><dd>{carryRate === null ? "—" : <><Num value={(carryRate * 100).toFixed(4)} decimals={4} signed directional /><span className="text-dim"> %/d</span></>}</dd></div>
			<div className="flex items-baseline justify-between"><dt className="text-dim">{t("arb.estimatedDailyCarry")}</dt><dd>{dailyCarry === null ? "—" : <><Num value={dailyCarry.toFixed(4)} decimals={4} signed directional /> <span className="text-dim">{quoteAsset}</span></>}</dd></div>
			<div className="flex items-baseline justify-between"><dt className="text-dim">{t("arb.estimatedFees")}</dt><dd className="text-dim">—</dd></div>
		  </dl>
		);
	}
  return (
    <dl className="flex flex-col gap-1 text-meta">
      <div className="flex items-baseline justify-between">
        <dt className="text-dim">{t("arb.spread")}</dt>
        <dd>
          <Num value={(spreadDaily * 100).toFixed(4)} decimals={4} signed directional />
          <span className="text-dim"> %/d</span>
        </dd>
      </div>
      <div className="flex items-baseline justify-between">
        {/* `APR` is a unit code, the same in every language. */}
        <dt className="text-dim">APR</dt>
        <dd>
          <Num value={(spreadDaily * 365 * 100).toFixed(2)} decimals={2} signed directional />
          <span className="text-dim"> %</span>
        </dd>
      </div>
      <div className="flex items-baseline justify-between">
        <dt className="text-dim">{t("arb.dailyFunding")}</dt>
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
      <div className="flex items-baseline justify-between"><dt className="text-dim">{t("arb.estimatedFees")}</dt><dd className="text-dim">—</dd></div>
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
  const t = useT();
  const { blockers, warnings, spreadDaily, spreadApr, basisRate, priceSpread, notional, dailyFunding } = sealed.preview;
  const blocked = blockers.length > 0;

  return (
    <div className="flex flex-col gap-2 rounded-card bg-fill p-2.5">
      <div className="flex items-center justify-between">
        <span className="th">{t("arb.confirm")}</span>
        <span className={expired ? "text-meta text-danger" : "text-meta text-dim"} role="timer" aria-live="off">
          {expired ? t("ticket.expired") : t("ticket.left", { duration: formatDuration(remaining) })}
        </span>
      </div>

      <dl className="flex flex-col gap-1 text-meta">
		{sealed.preview.strategy === "cross_perp_funding" ? <>
		<div className="flex items-baseline justify-between">
          <dt className="text-dim">{t("arb.sealedSpread")}</dt>
          <dd>
            <Num value={spreadDaily} decimals={6} signed directional />
            <span className="text-dim"> /d</span>
          </dd>
        </div>
		<div className="flex items-baseline justify-between">
		  <dt className="text-dim">{t("arb.sealedApr")}</dt>
		  <dd><Num value={spreadApr} decimals={4} signed directional /></dd>
		</div>
		<div className="flex items-baseline justify-between">
		  <dt className="text-dim">{t("arb.dailyFunding")}</dt>
		  <dd><Num value={dailyFunding} decimals={4} signed directional /></dd>
		</div>
		</> : <div className="flex items-baseline justify-between">
		  <dt className="text-dim">{sealed.preview.strategy === "spot_perp_basis" ? t("arb.basis") : t("arb.priceSpread")}</dt>
		  <dd><Num value={percentOf(basisRate ?? priceSpread) ?? undefined} decimals={4} signed directional /><span className="text-dim"> %</span></dd>
		</div>}
        <div className="flex items-baseline justify-between">
          <dt className="text-dim">{t("arb.notional")}</dt>
          <dd>
            <Num value={notional} decimals={2} grouped />
          </dd>
        </div>
      </dl>

      {/* Blockers and warnings are the server's own sentences, printed as they
          arrived — the same rule the pair rows follow. */}
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
          disabled={blocked || expired || opening}
          className="flex-1"
        >
          {opening ? t("arb.opening") : expired ? t("ticket.expiredButton") : t("arb.openBoth")}
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

function accountSupportsLeg(account: Account, leg: ArbitrageOpportunityLeg): boolean {
	if (account.venue !== leg.venue) return false;
	if (!account.routes || account.routes.length === 0) return true;
	return account.routes.some((route) => route.product === leg.product && route.requestedState === "active");
}

function availableMargin(balances: AccountBalance[] | undefined, quoteAsset: string): string | null {
  const balance = balances?.find((item) => item.asset === quoteAsset || item.settlementAsset === quoteAsset);
  return balance?.availableMargin ?? balance?.available ?? null;
}

function hedgeCapacity(longAvailable: string | null, longPrice: string | undefined, shortAvailable: string | null, shortPrice: string | undefined): number | null {
  const legs = [[longAvailable, longPrice], [shortAvailable, shortPrice]] as const;
  const capacities = legs.map(([available, price]) => {
    const funds = Number.parseFloat(available ?? "");
    const mark = Number.parseFloat(price ?? "");
    return Number.isFinite(funds) && Number.isFinite(mark) && funds > 0 && mark > 0 ? funds / mark : null;
  }).filter((value): value is number => value !== null);
  return capacities.length === 2 ? Math.min(...capacities) : null;
}

function openCapacity(available: string | null, price: string | undefined): number | null {
  const funds = Number.parseFloat(available ?? "");
  const mark = Number.parseFloat(price ?? "");
  return Number.isFinite(funds) && Number.isFinite(mark) && funds > 0 && mark > 0 ? funds / mark : null;
}

function numberOrNull(value: string | undefined): number | null {
  const parsed = Number.parseFloat(value ?? "");
  return Number.isFinite(parsed) ? parsed : null;
}

function quantityAtPercent(capacity: number, percent: number): string {
  return (capacity * percent / 100).toFixed(8).replace(/\.?0+$/, "");
}

function legNotional(quantity: string, price: string | undefined): number | null {
  const size = Number.parseFloat(quantity);
  const mark = Number.parseFloat(price ?? "");
  return Number.isFinite(size) && Number.isFinite(mark) && size > 0 && mark > 0 ? size * mark : null;
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

function priceEdgeOf(longPrice: string | undefined, shortPrice: string | undefined, strategy: ArbitrageStrategy): number | null {
	const long = Number.parseFloat(longPrice ?? "");
	const short = Number.parseFloat(shortPrice ?? "");
	if (!Number.isFinite(long) || !Number.isFinite(short) || long <= 0 || short <= 0) return null;
	const denominator = strategy === "spot_perp_basis" ? long : (long + short) / 2;
	return (short - long) / denominator;
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
