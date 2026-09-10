import { useEffect, useRef, useState, type CSSProperties } from "react";
import { ChevronDown, X } from "lucide-react";

import { describeError } from "@/api/errors";
import type { Account, AccountPosition, Instrument, ManualOrderRequest, OrderSide, OrderType } from "@/api/types";
import { preferencesStore, usePreferences } from "@/app/preferences";
import { Button } from "@/components/ui/button";
import { DialogClose, DialogContent, DialogRoot } from "@/components/ui/dialog";
import { Field, Input, inputClass } from "@/components/ui/field";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { useAccountBalances, usePlaceOrder } from "@/features/trade/queries";
import { describeCommandFailure } from "@/features/trade/order-status";
import { decimalsOf } from "@/features/market/precision";
import { compactIdentifier, formatDecimal, splitDecimal } from "@/lib/format";
import { useT, type Key } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * The order ticket.
 *
 * The confirmation is local: every value it shows already exists in the
 * ticket, catalog or public market stream. Confirming sends one command, and
 * Trading performs account, route, runtime-fence and venue-rule checks before
 * Gateway can reach the exchange.
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
  { value: "market", labelKey: "ticket.type.market" },
  { value: "limit", labelKey: "ticket.type.limit" },
];

const LEVERAGES = ["1", "2", "3", "5", "10", "20"] as const;
type QuantityUnit = "base" | "quote";

/** The confirm button and the success toast, which spell the side differently. */
const CONFIRM: Record<OrderSide, Key> = { buy: "ticket.confirm.buy", sell: "ticket.confirm.sell" };
const PLACE: Record<OrderSide, Key> = { buy: "ticket.place.buy", sell: "ticket.place.sell" };
const PLACED: Record<OrderSide, Key> = { buy: "ticket.placed.buy", sell: "ticket.placed.sell" };

export type OrderConfirmation = {
  request: ManualOrderRequest;
  referencePrice?: string;
  venue: string;
  accountLabel: string;
};

export function orderConfirmationRequired(confirmOrders: boolean): boolean {
  return confirmOrders;
}

export function OrderTicket({
  instrument,
  accounts,
  accountId,
  onAccountIdChange,
  positions,
  limitPrice,
  onLimitPriceChange,
  markPrice,
  bestBid,
  bestAsk,
}: {
  instrument: Instrument | null;
  /** Already filtered to the ones that can trade this instrument. */
  accounts: Account[];
  /** Shared with the blotter and chart so every private surface names one account. */
  accountId: string;
  onAccountIdChange: (next: string) => void;
  positions: AccountPosition[];
  /** Lifted so a click in the order book can fill it. */
  limitPrice: string;
  onLimitPriceChange: (next: string) => void;
  /** Fresh public mark, used only to initialize an empty limit-price field. */
  markPrice?: string;
  /** Live public prices used only for market-size conversion and estimates. */
  bestBid?: string;
  bestAsk?: string;
}) {
  const toast = useToast();
  const t = useT();
  const preferences = usePreferences();
  const [side, setSide] = useState<OrderSide>("buy");
  const [orderType, setOrderType] = useState<OrderType>("limit");
  const [amount, setAmount] = useState("");
  const [quantityUnit, setQuantityUnit] = useState<QuantityUnit>("base");
  const [sizePercent, setSizePercent] = useState(0);
  const [timeInForce, setTimeInForce] = useState("GTC");
  const [reduceOnly, setReduceOnly] = useState(false);
  const [leverage, setLeverage] = useState<(typeof LEVERAGES)[number]>("1");
  const [confirmation, setConfirmation] = useState<OrderConfirmation | null>(null);
  const seededLimitPriceForInstrument = useRef<string | null>(null);

  const place = usePlaceOrder();
  const balances = useAccountBalances(accountId);

  const positionQuantity = sumDecimals(
    positions.map((position) => position.quantity),
  );
  const longPositionQuantity = sumDecimals(
    positions
      .filter(
        (position) =>
          position.side === "buy" ||
          (position.side === undefined && !position.quantity.trimStart().startsWith("-")),
      )
      .map((position) => position.quantity),
  );
  const shortPositionQuantity = sumDecimals(
    positions
      .filter(
        (position) =>
          position.side === "sell" ||
          (position.side === undefined && position.quantity.trimStart().startsWith("-")),
      )
      .map((position) => absoluteDecimal(position.quantity)),
  );
  const settlementAsset = instrument?.settlement ?? instrument?.quoteAsset;
  const availableBalance = balances.data?.find(
    (balance) =>
      balance.product === instrument?.product &&
      (balance.settlementAsset?.toUpperCase() === settlementAsset?.toUpperCase() ||
        balance.asset.toUpperCase() === settlementAsset?.toUpperCase()),
  );
  const selectedAccount = accounts.find((account) => account.accountId === accountId);
  const quantityDecimals = decimalsOf(instrument?.rules.quantityIncrement);
  // Quote-sized market orders require the executable side of the book. Falling
  // back to mark or midpoint can overstate the quantity affordable at the ask.
  const marketReferencePrice = side === "buy"
    ? isPositiveDecimal(bestAsk ?? "") ? bestAsk ?? "" : ""
    : isPositiveDecimal(bestBid ?? "") ? bestBid ?? "" : "";
  const sizingPrice = orderType === "limit" ? limitPrice : marketReferencePrice;
  const baseQuantity = quantityUnit === "base"
    ? amount
    : quoteAmountToBaseQuantity(amount, sizingPrice, instrument?.rules.quantityIncrement) ?? "";
  const estimatedNotional = isPositiveDecimal(baseQuantity) && isPositiveDecimal(sizingPrice)
    ? multiplyPositiveDecimals(baseQuantity, sizingPrice)
    : null;
  const estimatedMargin = instrument?.product === "perp" && !reduceOnly && estimatedNotional
    ? dividePositiveDecimal(estimatedNotional, Number(leverage), 2)
    : null;
  const availableMargin = availableBalance?.availableMargin ?? availableBalance?.available;
  const closeablePositionQuantity = side === "sell" ? longPositionQuantity : shortPositionQuantity;
  const sliderAvailable = reduceOnly
    ? isPositiveDecimal(closeablePositionQuantity)
    : isPositiveDecimal(availableMargin ?? "") && isPositiveDecimal(sizingPrice);

  // Pick the only account, or the first, rather than making the operator choose
  // between one option. Re-runs when the instrument changes the candidate set.
  useEffect(() => {
    if (accounts.some((account) => account.accountId === accountId)) return;
    onAccountIdChange(accounts[0]?.accountId ?? "");
  }, [accounts, accountId, onAccountIdChange]);

  useEffect(() => {
    setAmount("");
    setSizePercent(0);
  }, [instrument?.instrumentId]);

  useEffect(() => {
    const instrumentId = instrument?.instrumentId ?? null;
    if (!instrumentId) {
      seededLimitPriceForInstrument.current = null;
      return;
    }
    if (orderType !== "limit" || seededLimitPriceForInstrument.current === instrumentId) return;
    if (limitPrice !== "") {
      seededLimitPriceForInstrument.current = instrumentId;
      return;
    }
    const next = defaultLimitPrice(markPrice, instrument?.rules.priceIncrement);
    if (!next) return;
    seededLimitPriceForInstrument.current = instrumentId;
    onLimitPriceChange(next);
  }, [instrument?.instrumentId, instrument?.rules.priceIncrement, limitPrice, markPrice, onLimitPriceChange, orderType]);

  // Any edit closes the confirmation so it can never submit values different
  // from the ones the operator reviewed.
  useEffect(() => {
    setConfirmation(null);
  }, [instrument?.instrumentId, accountId, side, orderType, amount, quantityUnit, limitPrice, timeInForce, reduceOnly, leverage]);

  const quantityError = amount !== "" && !isPositiveDecimal(amount)
    ? t("ticket.positive")
    : quantityUnit === "quote" && isPositiveDecimal(amount) && !isPositiveDecimal(sizingPrice)
      ? t("ticket.quantity.priceUnavailable")
      : quantityUnit === "quote" && isPositiveDecimal(amount) && !isPositiveDecimal(baseQuantity)
        ? t("ticket.quantity.tooSmall")
        : null;
  const priceError =
    orderType === "limit" && limitPrice !== "" && !isPositiveDecimal(limitPrice) ? t("ticket.positive") : null;

  const belowMinimum = notionalBelowMinimum(baseQuantity, sizingPrice, instrument?.rules.minNotional);
  const minimumMessage = belowMinimum
    ? t(orderType === "limit" ? "ticket.minimumNotional.limit" : "ticket.minimumNotional.market", {
        minimum: instrument?.rules.minNotional ?? "", quote: instrument?.quoteAsset ?? "",
      })
    : null;

  const ready =
    instrument !== null &&
    accountId !== "" &&
    isPositiveDecimal(baseQuantity) &&
    (orderType === "market" || (isPositiveDecimal(limitPrice) && !belowMinimum));
  const interactionLocked = place.isPending || confirmation !== null;

  const submitOrder = (next: OrderConfirmation) => {
    place.mutate(next.request, {
      onSuccess: (command) => {
        setConfirmation(null);
        if (command.state === "failed") {
          toast.push({
            tone: "danger",
            title: t("ticket.orderRejected"),
            detail: describeCommandFailure(command, t),
          });
          return;
        }
        setAmount("");
        setSizePercent(0);
        toast.push({
          tone: command.state === "uncertain" ? "warn" : "ok",
          // `command.state` is the server's own word and is not translated: it is
          // what the blotter row and the audit log call the same thing.
          title: t(PLACED[next.request.side], { state: command.state }),
          detail: compactIdentifier(command.commandId),
        });
      },
      onError: (error) => {
        setConfirmation(null);
        toast.push({ tone: "danger", title: t("ticket.orderRejected"), detail: describeError(error, t) });
      },
    });
  };

  const onPrepareOrder = () => {
    if (!instrument || !ready) return;
    const next: OrderConfirmation = {
      request: {
        accountId,
        instrumentId: instrument.instrumentId,
        side,
        orderType,
        quantity: baseQuantity,
        limitPrice: orderType === "limit" ? limitPrice : "",
        timeInForce,
        reduceOnly,
        leverage: instrument.product === "perp" ? Number(leverage) : 0,
      },
      referencePrice: orderType === "market" ? sizingPrice : undefined,
      venue: instrument.venue,
      accountLabel: selectedAccount ? `${selectedAccount.displayName} · ${selectedAccount.venue}` : accountId,
    };
    if (orderConfirmationRequired(preferences.confirmOrders)) {
      setConfirmation(next);
    } else {
      submitOrder(next);
    }
  };

  const onConfirm = () => {
    if (!confirmation) return;
    submitOrder(confirmation);
  };

  const changeQuantityUnit = (next: QuantityUnit) => {
    if (next === quantityUnit) return;
    const currentBaseQuantity = baseQuantity;
    setQuantityUnit(next);
    setSizePercent(0);
    if (!isPositiveDecimal(currentBaseQuantity)) {
      setAmount("");
      return;
    }
    setAmount(
      next === "base"
        ? currentBaseQuantity
        : multiplyPositiveDecimals(currentBaseQuantity, sizingPrice) ?? "",
    );
  };

  const changeSizePercent = (next: number) => {
    setSizePercent(next);
    if (next === 0) {
      setAmount("");
      return;
    }
    const nextBaseQuantity = reduceOnly
      ? positionPercentQuantity(closeablePositionQuantity, next, instrument?.rules.quantityIncrement)
      : marginPercentQuantity(
          availableMargin ?? "",
          sizingPrice,
          instrument?.product === "perp" ? Number(leverage) : 1,
          next,
          instrument?.rules.quantityIncrement,
        );
    if (!nextBaseQuantity) {
      setAmount("");
      return;
    }
    setAmount(
      quantityUnit === "base"
        ? nextBaseQuantity
        : multiplyPositiveDecimals(nextBaseQuantity, sizingPrice) ?? "",
    );
  };

  return (
    <section aria-label={t("ticket.title")} className="flex min-h-full flex-col bg-panel">
      <div className="flex h-bar shrink-0 items-center justify-between border-b border-line px-3">
        <h2 className="text-title font-semibold text-text">{t("ticket.title")}</h2>
        <span className="max-w-44 truncate text-meta text-dim" data-numeric="">
          {instrument ? `${instrument.symbol} · ${instrument.venue}` : "—"}
        </span>
      </div>

      <div className="flex flex-col gap-2.5 p-3">
        {/* Labels follow the selected language. */}
        <Segmented
          name="side"
          label={t("ticket.side")}
          value={side}
          options={SIDES.map((entry) => ({
            value: entry.value,
            label: t(entry.labelKey),
            tone: entry.tone,
            disabled: interactionLocked,
          }))}
          onChange={(next) => {
            setSide(next);
            setSizePercent(0);
          }}
        />

        <div className="grid grid-cols-2 gap-x-4 border-b border-line pb-3">
          <TicketStat
            label={t("ticket.availableMargin")}
            value={
              availableBalance
                ? `${formatDecimal(availableBalance.availableMargin ?? availableBalance.available, { decimals: 2 })} ${settlementAsset ?? availableBalance.asset}`
                : "—"
            }
          />
          <TicketStat
            label={t("ticket.position")}
            value={accountId && selectedAccount ? `${formatDecimal(positionQuantity, { decimals: quantityDecimals })} ${instrument?.baseAsset ?? ""}` : "—"}
          />
        </div>

        <Field label={t("ticket.account")} required>
          {(field) => (
            <select
              {...field}
              className={`${inputClass} h-row`}
              value={accountId}
              onChange={(event) => onAccountIdChange(event.target.value)}
              disabled={accounts.length === 0 || interactionLocked}
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

        <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
          <Segmented
            name="order-type"
            label={t("ticket.orderType")}
            value={orderType}
            className="min-w-0"
            trackClassName="border border-line-strong"
            emphasis="strong"
            options={ORDER_TYPES.map((entry) => ({
              value: entry.value,
              label: t(entry.labelKey),
              disabled: interactionLocked,
            }))}
            onChange={(next) => {
              setOrderType(next);
              setSizePercent(0);
              if (next === "limit" && limitPrice === "") {
                const nextPrice = defaultLimitPrice(markPrice, instrument?.rules.priceIncrement);
                if (nextPrice) {
                  seededLimitPriceForInstrument.current = instrument?.instrumentId ?? null;
                  onLimitPriceChange(nextPrice);
                }
              }
              // A resting market order is not a thing. Immediate-or-cancel is the
              // only honest time-in-force for one, so it is set and locked.
              setTimeInForce(next === "market" ? "IOC" : "GTC");
            }}
          />

          {instrument?.product === "perp" ? (
            <label className="grid shrink-0 grid-cols-[auto_72px] items-center gap-2 text-meta text-dim">
              <span>{t("ticket.leverage")}</span>
              <select
                aria-label={t("ticket.leverage")}
                className={`${inputClass} font-mono`}
                value={leverage}
                disabled={interactionLocked}
                onChange={(event) => {
                  setLeverage(event.target.value as (typeof LEVERAGES)[number]);
                  setSizePercent(0);
                }}
              >
                {LEVERAGES.map((value) => <option key={value} value={value}>{value}×</option>)}
              </select>
            </label>
          ) : null}
        </div>

        {orderType === "limit" ? (
          <Field
            label={`${t("ticket.price")}${instrument?.quoteAsset ? ` (${instrument.quoteAsset})` : ""}`}
            error={priceError}
            hint={t("ticket.price.hint")}
            required
          >
            {(field) => (
              <Input
                {...field}
                className="h-row text-right font-mono"
                inputMode="decimal"
                autoComplete="off"
                disabled={interactionLocked}
                value={limitPrice}
                placeholder="0.00"
                onChange={(event) => {
                  onLimitPriceChange(event.target.value);
                  setSizePercent(0);
                }}
              />
            )}
          </Field>
        ) : null}

        <Field
          label={t("ticket.quantity")}
          error={quantityError}
          hint={quantityHint({
            amount,
            unit: quantityUnit,
            baseQuantity,
            baseAsset: instrument?.baseAsset ?? "BTC",
            quoteAsset: instrument?.quoteAsset ?? "USD",
            quantityDecimals,
            priceAvailable: isPositiveDecimal(sizingPrice),
            t,
          })}
          required
        >
          {(field) => (
            <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-2">
              <Input
                {...field}
                className="h-row text-right font-mono"
                inputMode="decimal"
                autoComplete="off"
                disabled={interactionLocked}
                value={amount}
                placeholder="0.00"
                onChange={(event) => {
                  setAmount(event.target.value);
                  setSizePercent(0);
                }}
              />
              <QuantityUnitToggle
                value={quantityUnit}
                baseAsset={instrument?.baseAsset ?? "BTC"}
                quoteAsset={instrument?.quoteAsset ?? "USD"}
                disabled={interactionLocked}
                onChange={changeQuantityUnit}
              />
            </div>
          )}
        </Field>

        <PositionSlider
          value={sizePercent}
          side={side}
          disabled={!sliderAvailable || interactionLocked}
          reduceOnly={reduceOnly}
          onChange={changeSizePercent}
        />

        <details className="group border-y border-line">
          <summary className="flex h-row cursor-pointer list-none items-center justify-between text-base text-muted hover:text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent">
            <span>{t("ticket.advanced")}</span>
            <ChevronDown className="size-4 transition-transform group-open:rotate-180" aria-hidden="true" />
          </summary>
          <div className="flex items-end gap-3 pb-3 pt-1">
            <Field label={t("ticket.tif")} className="flex-1">
              {(field) => (
                <select
                  {...field}
                  className={`${inputClass} h-row`}
                  value={timeInForce}
                  onChange={(event) => setTimeInForce(event.target.value)}
                  disabled={orderType === "market" || interactionLocked}
                >
                  <option value="GTC">GTC</option>
                  <option value="IOC">IOC</option>
                  <option value="FOK">FOK</option>
                </select>
              )}
            </Field>

            <label className="flex h-[30px] shrink-0 items-center gap-2 text-base text-muted">
              <input
                type="checkbox"
                disabled={interactionLocked}
                checked={reduceOnly}
                onChange={(event) => {
                  setReduceOnly(event.target.checked);
                  setSizePercent(0);
                }}
                className="size-3.5 accent-[var(--bt-accent)]"
              />
              {t("ticket.reduceOnly")}
            </label>
          </div>
        </details>

        {minimumMessage ? <p role="status" className="text-meta text-warn">{minimumMessage}</p> : null}

        <Button
          variant={side}
          size="lg"
          onClick={onPrepareOrder}
          disabled={!ready || place.isPending || confirmation !== null}
          className="w-full"
        >
          {place.isPending ? t("ticket.placing") : t(PLACE[side])}
        </Button>

        <div className="flex flex-col gap-1 border-t border-line pt-2" aria-label={t("ticket.preview.live")}>
          <div className="mb-1 flex items-center justify-between">
            <span className="th">{t("ticket.preview.live")}</span>
            <span className="text-meta text-dim">{t("ticket.preview.estimate")}</span>
          </div>
          <TicketStat
            label={t("ticket.preview.orderQuantity")}
            value={
              isPositiveDecimal(baseQuantity)
                ? `${formatDecimal(baseQuantity, { decimals: quantityDecimals })} ${instrument?.baseAsset ?? ""}`
                : "—"
            }
          />
          <TicketStat
            label={orderType === "market" ? t("ticket.preview.referencePrice") : t("ticket.price")}
            value={
              isPositiveDecimal(sizingPrice)
                ? `${orderType === "market" ? "≈ " : ""}${formatDecimal(sizingPrice, { decimals: decimalsOf(instrument?.rules.priceIncrement) })} ${instrument?.quoteAsset ?? ""}`
                : orderType === "market" ? t("ticket.preview.waitingMarket") : "—"
            }
          />
          <TicketStat
            label={t("ticket.notional")}
            value={
              estimatedNotional
                ? `${orderType === "market" ? "≈ " : ""}${formatDecimal(estimatedNotional, { decimals: 2 })} ${instrument?.quoteAsset ?? ""}`
                : "—"
            }
          />
          {isPositiveDecimal(instrument?.rules.minNotional ?? "") ? (
            <TicketStat label={t("ticket.minimumNotional")} value={`${instrument?.rules.minNotional} ${instrument?.quoteAsset ?? ""}`} />
          ) : null}
          <TicketStat
            label={t("ticket.estimatedLiquidation")}
            value={estimatedNotional && instrument?.product === "perp" ? t("ticket.preview.afterFill") : "—"}
          />
          <TicketStat
            label={t("ticket.positionMargin")}
            value={
              instrument?.product !== "perp" || reduceOnly
                ? t("ticket.preview.notApplicable")
                : estimatedMargin
                  ? `≈ ${formatDecimal(estimatedMargin, { decimals: 2 })} ${settlementAsset ?? instrument?.quoteAsset ?? ""}`
                  : "—"
            }
          />
          <TicketStat label={t("ticket.fee")} value={estimatedNotional ? t("ticket.preview.venueConfirm") : "—"} />
        </div>

      </div>

      {confirmation ? (
          <OrderConfirmationDialog
            confirmation={confirmation}
            side={confirmation.request.side}
            placing={place.isPending}
            instrument={instrument}
            referencePrice={confirmation.referencePrice}
            accountLabel={confirmation.accountLabel}
            onConfirm={onConfirm}
            onDiscard={() => setConfirmation(null)}
          />
        ) : null}
    </section>
  );
}

function QuantityUnitToggle({
  value,
  baseAsset,
  quoteAsset,
  disabled,
  onChange,
}: {
  value: QuantityUnit;
  baseAsset: string;
  quoteAsset: string;
  disabled: boolean;
  onChange: (next: QuantityUnit) => void;
}) {
  const t = useT();
  const options: { value: QuantityUnit; label: string }[] = [
    { value: "base", label: baseAsset },
    { value: "quote", label: quoteAsset },
  ];
  return (
    <div
      className="flex h-row items-center rounded-control bg-fill p-0.5"
      role="group"
      aria-label={t("ticket.quantity.unit")}
    >
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          disabled={disabled}
          aria-pressed={value === option.value}
          onClick={() => onChange(option.value)}
          className={
            value === option.value
              ? "h-full rounded-control bg-raised px-2 font-mono text-meta text-text disabled:opacity-50"
              : "h-full rounded-control px-2 font-mono text-meta text-dim hover:text-text disabled:opacity-50"
          }
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

function PositionSlider({
  value,
  side,
  disabled,
  reduceOnly,
  onChange,
}: {
  value: number;
  side: OrderSide;
  disabled: boolean;
  reduceOnly: boolean;
  onChange: (next: number) => void;
}) {
  const t = useT();
  const style = {
    "--slider-fill": `${value}%`,
    "--slider-color": side === "buy" ? "var(--bt-buy)" : "var(--bt-sell)",
  } as CSSProperties;
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-3">
        <span className="th">{t("ticket.positionPercent")}</span>
        <span className="text-meta text-dim">
          {t(reduceOnly ? "ticket.positionPercent.reduce" : "ticket.positionPercent.margin")}
        </span>
      </div>
      <div className="flex items-center gap-2">
        <input
          type="range"
          min="0"
          max="100"
          step="1"
          value={value}
          disabled={disabled}
          aria-label={t("ticket.positionPercent")}
          aria-valuetext={`${value}%`}
          onChange={(event) => onChange(Number(event.target.value))}
          className="position-slider min-w-0 flex-1"
          style={style}
        />
        <output className="flex h-[30px] min-w-14 items-center justify-end rounded-control border border-line-strong bg-raised px-2 font-mono text-base text-text">
          {value}%
        </output>
      </div>
    </div>
  );
}

function quantityHint({
  amount,
  unit,
  baseQuantity,
  baseAsset,
  quoteAsset,
  quantityDecimals,
  priceAvailable,
  t,
}: {
  amount: string;
  unit: QuantityUnit;
  baseQuantity: string;
  baseAsset: string;
  quoteAsset: string;
  quantityDecimals?: number;
  priceAvailable: boolean;
  t: (key: Key, values?: Record<string, string | number>) => string;
}): string | undefined {
  if (unit === "base") return undefined;
  if (isPositiveDecimal(amount) && isPositiveDecimal(baseQuantity)) {
    return t("ticket.quantity.converted", {
      quantity: formatDecimal(baseQuantity, { decimals: quantityDecimals }),
      base: baseAsset,
    });
  }
  if (!priceAvailable) return t("ticket.quantity.awaitPrice", { quote: quoteAsset });
  return t("ticket.quantity.quoteHint", { quote: quoteAsset, base: baseAsset });
}

/* --- Confirmation -------------------------------------------------------- */

export function OrderConfirmationDialog({
  confirmation,
  side,
  placing,
  instrument,
  referencePrice,
  accountLabel,
  titleKey,
  confirmLabelKey,
  orderLabelKey,
  onConfirm,
  onDiscard,
}: {
  confirmation: OrderConfirmation;
  side: OrderSide;
  placing: boolean;
  instrument: Instrument | null;
  referencePrice?: string;
  accountLabel: string;
  /** Lets another manual-order entry point keep the same confirmation UI. */
  titleKey?: Key;
  confirmLabelKey?: Key;
  /** Replaces buy/long or sell/short when the intent is explicitly a close. */
  orderLabelKey?: Key;
  onConfirm: () => void;
  onDiscard: () => void;
}) {
  const t = useT();
  const preferences = usePreferences();
  const request = confirmation.request;
  const quantityDecimals = decimalsOf(instrument?.rules.quantityIncrement);
  const priceDecimals = decimalsOf(instrument?.rules.priceIncrement);
  const quantityAsset = instrument?.baseAsset ?? "";
  const quoteAsset = instrument?.quoteAsset ?? "";
  const notionalPrice = request.orderType === "limit" ? request.limitPrice : referencePrice;
  const notional = notionalPrice
    ? multiplyPositiveDecimals(request.quantity, notionalPrice)
    : null;
  const orderParts = [
    t(orderLabelKey ?? (request.side === "buy" ? "ticket.side.buy" : "ticket.side.sell")),
    t(request.orderType === "limit" ? "ticket.type.limit" : "ticket.type.market"),
  ];
  if (request.leverage > 0) orderParts.push(`${request.leverage}×`);
  if (request.reduceOnly) orderParts.push(t("ticket.reduceOnly"));

  return (
    <DialogRoot open onOpenChange={(open) => {
      if (!open && !placing) onDiscard();
    }}>
      <DialogContent
        title={t(titleKey ?? "ticket.preview.summary")}
        description={t("ticket.confirm.description")}
        className="max-w-lg"
        onEscapeKeyDown={(event) => {
          if (placing) event.preventDefault();
        }}
        onPointerDownOutside={(event) => {
          if (placing) event.preventDefault();
        }}
      >
        <DialogClose asChild>
          <Button
            variant="ghost"
            size="icon"
            className="absolute top-2.5 right-2.5"
            aria-label={t("common.close")}
            disabled={placing}
          >
            <X aria-hidden className="size-4" />
          </Button>
        </DialogClose>

        <div className="overflow-y-auto px-4 py-3">
          <div className="mb-3 rounded-card bg-fill px-3 py-2">
            <span className={side === "buy" ? "font-medium text-buy" : "font-medium text-sell"}>
              {orderParts.join(" · ")}
            </span>
          </div>

          <dl className="flex flex-col gap-1.5 text-base">
            <PreviewStat label={t("ticket.account")} value={accountLabel} />
            <PreviewStat label={t("ticket.preview.order")} value={orderParts.join(" · ")} />
            <PreviewStat
              label={t("ticket.quantity")}
              value={`${formatDecimal(request.quantity, { decimals: quantityDecimals })} ${quantityAsset}`}
              numeric
            />
            <PreviewStat
              label={t("ticket.price")}
              value={request.orderType === "market" && referencePrice
                ? `≈ ${formatDecimal(referencePrice, { decimals: priceDecimals })} ${quoteAsset}`
                : request.orderType === "market"
                  ? t("ticket.preview.marketPrice")
                  : `${formatDecimal(request.limitPrice, { decimals: priceDecimals })} ${quoteAsset}`}
              numeric={request.orderType === "limit" || Boolean(referencePrice)}
            />
            <PreviewStat
              label={t("ticket.notional")}
              value={notional
                ? `${request.orderType === "market" ? "≈ " : ""}${formatDecimal(notional, { decimals: 2 })} ${quoteAsset}`
                : t("ticket.preview.marketPrice")}
              numeric={Boolean(notional)}
            />
          </dl>
        </div>

        <div className="border-t border-line bg-panel px-4 py-3">
          <label className="mb-3 flex w-fit cursor-pointer items-center gap-2 text-meta text-muted">
            <input
              type="checkbox"
              checked={!preferences.confirmOrders}
              onChange={(event) => preferencesStore.setConfirmOrders(!event.target.checked)}
              className="size-3.5 accent-[var(--bt-accent)]"
            />
            {t("ticket.confirm.dontShowAgain")}
          </label>
          <div className="flex gap-2">
            <DialogClose asChild>
              <Button variant="ghost" size="lg" disabled={placing} className="shrink-0">
                {t("ticket.discard")}
              </Button>
            </DialogClose>
            <Button
              variant={side}
              size="lg"
              onClick={onConfirm}
              disabled={placing}
              className="flex-1"
            >
              {placing ? t("ticket.placing") : t(confirmLabelKey ?? CONFIRM[side])}
            </Button>
          </div>
        </div>
      </DialogContent>
    </DialogRoot>
  );
}

function PreviewStat({ label, value, numeric = false }: { label: string; value: string; numeric?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <dt className="text-dim">{label}</dt>
      <dd className="max-w-52 text-right text-muted" data-numeric={numeric ? "" : undefined}>{value}</dd>
    </div>
  );
}

/* --- Helpers ------------------------------------------------------------- */

function isPositiveDecimal(value: string): boolean {
  const parts = splitDecimal(value);
  if (!parts || parts.sign === "-") return false;
  return !/^0*$/.test(parts.integer) || !/^0*$/.test(parts.fraction);
}

/** Fresh mark snapped to the nearest venue tick for a safe limit-price default. */
export function defaultLimitPrice(markPrice: string | undefined, increment: unknown): string | null {
  const mark = markPrice ? positiveDecimalUnits(markPrice) : null;
  const step = typeof increment === "string" ? positiveDecimalUnits(increment) : null;
  if (!mark || !step) return null;
  const scale = Math.max(mark.scale, step.scale);
  const markUnits = mark.units * 10n ** BigInt(scale - mark.scale);
  const stepUnits = step.units * 10n ** BigInt(scale - step.scale);
  const snapped = ((markUnits + stepUnits / 2n) / stepUnits) * stepUnits;
  return snapped > 0n ? unitsToDecimal(snapped, scale) : null;
}

/** Exact decimal addition for position rows that may be split by lineage. */
export function sumDecimals(values: string[]): string {
  const parsed = values.map(splitDecimal).filter((value): value is NonNullable<typeof value> => value !== null);
  if (parsed.length === 0) return "0";
  const scale = Math.max(...parsed.map((value) => value.fraction.length));
  const total = parsed.reduce((sum, value) => {
    const magnitude = BigInt(value.integer + value.fraction.padEnd(scale, "0"));
    return sum + (value.sign === "-" ? -magnitude : magnitude);
  }, 0n);
  return unitsToDecimal(total, scale);
}

/** A position percentage, floored onto the venue quantity increment. */
export function positionPercentQuantity(position: string, percent: number, increment: unknown): string | null {
  const value = splitDecimal(position);
  const step = typeof increment === "string" ? splitDecimal(increment) : null;
  if (!value || value.sign === "-" || !step || step.sign === "-" || percent <= 0 || percent > 100) return null;
  const scale = Math.max(value.fraction.length, step.fraction.length);
  const units = BigInt(value.integer + value.fraction.padEnd(scale, "0"));
  const stepUnits = BigInt(step.integer + step.fraction.padEnd(scale, "0"));
  if (units <= 0n || stepUnits <= 0n) return null;
  const proportional = (units * BigInt(percent)) / 100n;
  const rounded = (proportional / stepUnits) * stepUnits;
  return rounded > 0n ? unitsToDecimal(rounded, scale) : null;
}

/** Quote-currency amount converted to base quantity on the venue lot grid. */
export function quoteAmountToBaseQuantity(quoteAmount: string, price: string, increment: unknown): string | null {
  const quote = positiveDecimalUnits(quoteAmount);
  const executionPrice = positiveDecimalUnits(price);
  const step = typeof increment === "string" ? positiveDecimalUnits(increment) : null;
  if (!quote || !executionPrice || !step) return null;

  const numerator = quote.units * 10n ** BigInt(executionPrice.scale + step.scale);
  const denominator = executionPrice.units * step.units * 10n ** BigInt(quote.scale);
  const steps = numerator / denominator;
  return steps > 0n ? unitsToDecimal(steps * step.units, step.scale) : null;
}

/** Available margin × leverage × percentage, converted to a safe base quantity. */
export function marginPercentQuantity(
  availableMargin: string,
  price: string,
  leverage: number,
  percent: number,
  increment: unknown,
): string | null {
  const margin = positiveDecimalUnits(availableMargin);
  if (!margin || !Number.isInteger(leverage) || leverage <= 0 || !Number.isInteger(percent) || percent <= 0 || percent > 100) {
    return null;
  }
  const notionalUnits = (margin.units * BigInt(leverage) * BigInt(percent)) / 100n;
  return quoteAmountToBaseQuantity(unitsToDecimal(notionalUnits, margin.scale), price, increment);
}

function positiveDecimalUnits(value: string): { units: bigint; scale: number } | null {
  const parts = splitDecimal(value);
  if (!parts || parts.sign === "-" || !isPositiveDecimal(value)) return null;
  return {
    units: BigInt(parts.integer + parts.fraction),
    scale: parts.fraction.length,
  };
}

export function multiplyPositiveDecimals(left: string, right: string): string | null {
  const a = positiveDecimalUnits(left);
  const b = positiveDecimalUnits(right);
  if (!a || !b) return null;
  return unitsToDecimal(a.units * b.units, a.scale + b.scale);
}

function dividePositiveDecimal(value: string, divisor: number, places: number): string | null {
  const parsed = positiveDecimalUnits(value);
  if (!parsed || !Number.isInteger(divisor) || divisor <= 0 || places < 0) return null;
  const scaleDelta = places - parsed.scale;
  const numerator = scaleDelta >= 0 ? parsed.units * 10n ** BigInt(scaleDelta) : parsed.units;
  const denominator = BigInt(divisor) * (scaleDelta < 0 ? 10n ** BigInt(-scaleDelta) : 1n);
  const rounded = (numerator + denominator / 2n) / denominator;
  return unitsToDecimal(rounded, places);
}

function absoluteDecimal(value: string): string {
  const parts = splitDecimal(value);
  if (!parts) return "0";
  return unitsToDecimal(BigInt(parts.integer + parts.fraction), parts.fraction.length);
}

function unitsToDecimal(value: bigint, scale: number): string {
  const sign = value < 0n ? "-" : "";
  const digits = (value < 0n ? -value : value).toString().padStart(scale + 1, "0");
  if (scale === 0) return sign + digits;
  const fraction = digits.slice(-scale).replace(/0+$/, "");
  return sign + digits.slice(0, -scale) + (fraction === "" ? "" : `.${fraction}`);
}

function TicketStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-h-5 flex-col items-start gap-1 text-meta text-dim">
      <span>{label}</span>
      <span className="whitespace-nowrap text-base text-text" data-numeric="">
        {value}
      </span>
    </div>
  );
}

/** Exact comparison, including sub-cent values and values beyond Number precision. */
export function notionalBelowMinimum(quantity: string, price: string, minimum: unknown): boolean {
  const notional = multiplyPositiveDecimals(quantity, price);
  const amount = notional ? positiveDecimalUnits(notional) : null;
  const limit = typeof minimum === "string" ? positiveDecimalUnits(minimum) : null;
  if (!amount || !limit) return false;
  const scale = Math.max(amount.scale, limit.scale);
  return amount.units * 10n ** BigInt(scale - amount.scale) < limit.units * 10n ** BigInt(scale - limit.scale);
}
