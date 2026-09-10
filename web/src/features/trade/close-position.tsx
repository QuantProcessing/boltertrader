import { useState, type CSSProperties } from "react";
import { X } from "lucide-react";

import { describeError } from "@/api/errors";
import type { AccountPosition, Instrument, ManualOrderRequest, OrderSide, OrderType } from "@/api/types";
import { Button } from "@/components/ui/button";
import { DialogClose, DialogContent, DialogRoot } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { useToast } from "@/components/ui/toast";
import { decimalsOf } from "@/features/market/precision";
import { positionUnrealizedPnl } from "@/features/trade/live-position";
import {
  defaultLimitPrice,
  multiplyPositiveDecimals,
  positionPercentQuantity,
  sumDecimals,
} from "@/features/trade/order-ticket";
import { useAccountBalances, usePlaceOrder } from "@/features/trade/queries";
import { describeCommandFailure } from "@/features/trade/order-status";
import { compactIdentifier, formatDecimal, splitDecimal } from "@/lib/format";
import { useT } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * Closing an account position.
 *
 * The close ticket is an editor and confirmation in one. Every estimate comes
 * from the account position, catalog rules and the public mark already held by
 * the browser; opening it never makes a preview request. Submission is one
 * normal reduce-only order, so the venue remains the final authority and a
 * stale quantity cannot accidentally reverse the position.
 * ------------------------------------------------------------------------- */

interface CloseOrderInput {
  quantity?: string;
  orderType?: OrderType;
  limitPrice?: string;
}

export function closePositionOrderRequest(
  position: AccountPosition,
  input: CloseOrderInput = {},
): ManualOrderRequest | null {
  const positionQuantity = positiveMagnitude(position.quantity);
  const quantity = positiveMagnitude(input.quantity ?? position.quantity);
  const orderType = input.orderType ?? "market";
  const limitPrice = orderType === "limit" ? positiveMagnitude(input.limitPrice ?? "") : "";
  if (
    !positionQuantity ||
    !quantity ||
    comparePositiveDecimals(quantity, positionQuantity) > 0 ||
    (orderType === "limit" && !limitPrice) ||
    position.accountId === "" ||
    position.instrumentId === ""
  ) return null;
  return {
    accountId: position.accountId,
    instrumentId: position.instrumentId,
    side: closingSide(position),
    orderType,
    quantity,
    limitPrice: limitPrice ?? "",
    timeInForce: orderType === "market" ? "IOC" : "GTC",
    reduceOnly: true,
    // Closing does not change leverage. Zero is the server's explicit
    // "unchanged/not applicable" value for perpetual requests.
    leverage: 0,
  };
}

export function PositionCloseControl({
  position,
  instrument,
  referencePrice,
}: {
  position: AccountPosition;
  instrument: Instrument | null;
  referencePrice?: string;
}) {
  const t = useT();
  const toast = useToast();
  const place = usePlaceOrder();
  const balances = useAccountBalances(position.accountId);
  const [open, setOpen] = useState(false);
  const [quantity, setQuantity] = useState("");
  const [percent, setPercent] = useState(100);
  const [orderType, setOrderType] = useState<OrderType>("market");
  const [limitPrice, setLimitPrice] = useState("");

  const fullQuantity = positiveMagnitude(position.quantity) ?? "";
  const price = orderType === "limit" ? limitPrice : positiveMagnitude(referencePrice ?? position.markPrice) ?? "";
  const request = closePositionOrderRequest(position, { quantity, orderType, limitPrice });
  const unavailable = closeUnavailableReason(position, fullQuantity !== "", t("blotter.closeUnavailable"));
  const quantityError = quantity !== "" && request === null ? t("blotter.closeQuantityInvalid") : null;
  const remaining = request ? sumDecimals([fullQuantity, `-${request.quantity}`]) : fullQuantity;
  const estimatedNotional = request && price ? multiplyPositiveDecimals(request.quantity, price) : null;
  const pnl = request && price
    ? positionUnrealizedPnl({ ...position, quantity: signedCloseQuantity(position, request.quantity) }, price)
    : null;
  const estimatedRealized = pnl?.estimated ? pnl.value : "";
  const settlementAsset = position.settlementAsset ?? instrument?.settlement ?? instrument?.quoteAsset ?? "";
  const availableBalance = balances.data?.find((balance) =>
    balance.product === (position.product ?? instrument?.product) &&
    (balance.settlementAsset?.toUpperCase() === settlementAsset.toUpperCase() ||
      balance.asset.toUpperCase() === settlementAsset.toUpperCase()),
  );
  const availableMargin = availableBalance?.availableMargin ?? availableBalance?.available;
  const quantityDecimals = decimalsOf(instrument?.rules.quantityIncrement);
  const priceDecimals = decimalsOf(instrument?.rules.priceIncrement);
  const closingOrderSide = closingSide(position);

  const show = () => {
    if (unavailable) return;
    setQuantity(fullQuantity);
    setPercent(100);
    setOrderType("market");
    setLimitPrice(defaultLimitPrice(referencePrice ?? position.markPrice, instrument?.rules.priceIncrement) ?? "");
    setOpen(true);
  };

  const changePercent = (next: number) => {
    setPercent(next);
    setQuantity(closePercentQuantity(fullQuantity, next, instrument?.rules.quantityIncrement) ?? "");
  };

  const changeQuantity = (next: string) => {
    setQuantity(next);
    setPercent(positionQuantityPercent(next, fullQuantity));
  };

  const submit = () => {
    if (!request) return;
    place.mutate(request, {
      onSuccess: (command) => {
        setOpen(false);
        if (command.state === "failed") {
          toast.push({
            tone: "danger",
            title: t("blotter.closeRejected"),
            detail: describeCommandFailure(command, t),
          });
          return;
        }
        toast.push({
          tone: command.state === "uncertain" ? "warn" : "ok",
          title: t("blotter.closeSubmitted", { state: command.state }),
          detail: compactIdentifier(command.commandId),
        });
      },
      onError: (error) => {
        setOpen(false);
        toast.push({ tone: "danger", title: t("blotter.closeRejected"), detail: describeError(error, t) });
      },
    });
  };

  const sliderStyle = {
    "--slider-fill": `${percent}%`,
    "--slider-color": closingOrderSide === "buy" ? "var(--bt-buy)" : "var(--bt-sell)",
  } as CSSProperties;

  return (
    <>
      <Button
        variant="danger"
        size="sm"
        disabled={Boolean(unavailable) || place.isPending}
        title={unavailable || undefined}
        onClick={show}
      >
        {t("blotter.close")}
      </Button>

      {open ? (
        <DialogRoot open onOpenChange={(next) => {
          if (!next && !place.isPending) setOpen(false);
        }}>
          <DialogContent
            title={t("blotter.closeConfirmTitle")}
            description={t("blotter.closeDescription")}
            descriptionHidden
            className="max-w-lg"
            onEscapeKeyDown={(event) => {
              if (place.isPending) event.preventDefault();
            }}
            onPointerDownOutside={(event) => {
              if (place.isPending) event.preventDefault();
            }}
          >
            <DialogClose asChild>
              <Button
                variant="ghost"
                size="icon"
                className="absolute top-2.5 right-2.5"
                aria-label={t("common.close")}
                disabled={place.isPending}
              >
                <X aria-hidden className="size-4" />
              </Button>
            </DialogClose>

            <div className="mt-3 overflow-y-auto border-t border-line">
              <dl className="grid gap-1.5 px-4 py-3 text-base">
                <CloseStat
                  label={t("ticket.availableMargin")}
                  value={availableMargin
                    ? `${formatDecimal(availableMargin, { decimals: 2 })} ${settlementAsset}`
                    : "—"}
                />
                <CloseStat
                  label={t("ticket.position")}
                  value={`${formatDecimal(fullQuantity, { decimals: quantityDecimals })} ${instrument?.baseAsset ?? ""}`}
                  tone={closingOrderSide === "sell" ? "buy" : "sell"}
                />
              </dl>

              <div className="flex flex-col gap-3 border-y border-line bg-fill/40 px-4 py-3">
                <Field label={t("ticket.quantity")} error={quantityError} required>
                  {(field) => (
                    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center overflow-hidden rounded-control border border-line-strong bg-raised">
                      <Input
                        {...field}
                        className="h-row border-0 bg-transparent text-right font-mono"
                        inputMode="decimal"
                        autoComplete="off"
                        disabled={place.isPending}
                        value={quantity}
                        onChange={(event) => changeQuantity(event.target.value)}
                      />
                      <span className="px-3 font-mono text-base text-muted">{instrument?.baseAsset ?? ""}</span>
                    </div>
                  )}
                </Field>

                <div className="flex items-center gap-2">
                  <input
                    type="range"
                    min="0"
                    max="100"
                    step="1"
                    value={percent}
                    disabled={place.isPending}
                    aria-label={t("ticket.positionPercent")}
                    aria-valuetext={`${percent}%`}
                    onChange={(event) => changePercent(Number(event.target.value))}
                    className="position-slider min-w-0 flex-1"
                    style={sliderStyle}
                  />
                  <output className="flex h-[30px] min-w-14 items-center justify-end rounded-control border border-line-strong bg-raised px-2 font-mono text-base text-text">
                    {percent}%
                  </output>
                </div>

                <label className="flex w-fit cursor-pointer items-center gap-2 text-base text-muted">
                  <input
                    type="checkbox"
                    checked={orderType === "limit"}
                    disabled={place.isPending}
                    onChange={(event) => setOrderType(event.target.checked ? "limit" : "market")}
                    className="size-3.5 accent-[var(--bt-accent)]"
                  />
                  {t("ticket.type.limit")}
                </label>

                {orderType === "limit" ? (
                  <Field label={`${t("ticket.price")} (${instrument?.quoteAsset ?? ""})`} required>
                    {(field) => (
                      <Input
                        {...field}
                        className="h-row text-right font-mono"
                        inputMode="decimal"
                        autoComplete="off"
                        disabled={place.isPending}
                        value={limitPrice}
                        onChange={(event) => setLimitPrice(event.target.value)}
                      />
                    )}
                  </Field>
                ) : null}

                <Button
                  variant={closingOrderSide}
                  size="lg"
                  className="w-full"
                  disabled={!request || place.isPending}
                  onClick={submit}
                >
                  {place.isPending ? t("ticket.placing") : t("blotter.closeConfirm")}
                </Button>
              </div>

              <dl className="grid gap-1.5 px-4 py-3 text-base">
                <CloseStat
                  label={t("ticket.position")}
                  value={`${formatDecimal(fullQuantity, { decimals: quantityDecimals })} → ${formatDecimal(remaining, { decimals: quantityDecimals })} ${instrument?.baseAsset ?? ""}`}
                />
                <CloseStat
                  label={t("blotter.closeEstimatedPrice")}
                  value={price
                    ? `${orderType === "market" ? "≈ " : ""}${formatDecimal(price, { decimals: priceDecimals })} ${instrument?.quoteAsset ?? ""}`
                    : "—"}
                />
                <CloseStat
                  label={t("blotter.closeEstimatedNotional")}
                  value={estimatedNotional
                    ? `${orderType === "market" ? "≈ " : ""}${formatDecimal(estimatedNotional, { decimals: 2 })} ${instrument?.quoteAsset ?? ""}`
                    : "—"}
                />
                <CloseStat
                  label={t("blotter.closeEstimatedRealizedPnl")}
                  value={estimatedRealized
                    ? `${formatDecimal(estimatedRealized, { decimals: 2, signed: true })} ${settlementAsset}`
                    : "—"}
                  tone={decimalTone(estimatedRealized)}
                />
              </dl>
            </div>
          </DialogContent>
        </DialogRoot>
      ) : null}
    </>
  );
}

function CloseStat({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: "buy" | "sell";
}) {
  return (
    <div className="flex items-start justify-between gap-3">
      <dt className="text-dim">{label}</dt>
      <dd
        className={tone === "buy" ? "text-right font-mono text-buy" : tone === "sell" ? "text-right font-mono text-sell" : "text-right font-mono text-text"}
        data-numeric=""
      >
        {value}
      </dd>
    </div>
  );
}

function closingSide(position: AccountPosition): OrderSide {
  const side = position.side?.toLowerCase();
  if (side === "sell" || side === "short") return "buy";
  if (side === "buy" || side === "long") return "sell";
  return position.quantity.trimStart().startsWith("-") ? "buy" : "sell";
}

function signedCloseQuantity(position: AccountPosition, quantity: string): string {
  return closingSide(position) === "buy" ? `-${quantity}` : quantity;
}

function positiveMagnitude(value: string): string | null {
  const parts = splitDecimal(value);
  if (!parts) return null;
  const integer = parts.integer.replace(/^0+(?=\d)/, "");
  const fraction = parts.fraction.replace(/0+$/, "");
  if (/^0+$/.test(integer) && fraction === "") return null;
  return fraction ? `${integer}.${fraction}` : integer;
}

function comparePositiveDecimals(left: string, right: string): number {
  const a = splitDecimal(left);
  const b = splitDecimal(right);
  if (!a || !b) return 0;
  const scale = Math.max(a.fraction.length, b.fraction.length);
  const leftUnits = BigInt(a.integer + a.fraction.padEnd(scale, "0"));
  const rightUnits = BigInt(b.integer + b.fraction.padEnd(scale, "0"));
  return leftUnits < rightUnits ? -1 : leftUnits > rightUnits ? 1 : 0;
}

export function positionQuantityPercent(quantity: string, fullQuantity: string): number {
  const value = positiveMagnitude(quantity);
  const total = positiveMagnitude(fullQuantity);
  if (!value || !total) return 0;
  const a = splitDecimal(value)!;
  const b = splitDecimal(total)!;
  const scale = Math.max(a.fraction.length, b.fraction.length);
  const units = BigInt(a.integer + a.fraction.padEnd(scale, "0"));
  const totalUnits = BigInt(b.integer + b.fraction.padEnd(scale, "0"));
  if (totalUnits <= 0n) return 0;
  const percent = Number((units * 100n) / totalUnits);
  return Math.max(0, Math.min(100, percent));
}

function closePercentQuantity(fullQuantity: string, percent: number, increment: unknown): string | null {
  const quantized = positionPercentQuantity(fullQuantity, percent, increment);
  if (quantized || typeof increment === "string") return quantized;
  const value = positiveMagnitude(fullQuantity);
  if (!value || !Number.isInteger(percent) || percent <= 0 || percent > 100) return null;
  const parts = splitDecimal(value)!;
  const units = (BigInt(parts.integer + parts.fraction) * BigInt(percent)) / 100n;
  if (units <= 0n) return null;
  const digits = units.toString().padStart(parts.fraction.length + 1, "0");
  if (parts.fraction.length === 0) return digits;
  const fraction = digits.slice(-parts.fraction.length).replace(/0+$/, "");
  return `${digits.slice(0, -parts.fraction.length)}${fraction ? `.${fraction}` : ""}`;
}

function closeUnavailableReason(
  position: AccountPosition,
  validPosition: boolean,
  fallback: string,
): string | null {
  if (!validPosition) return fallback;
  // Account realtime health is not write authority. A temporary private-WS gap
  // may make the row stale, but reduce-only submission remains safe and the
  // fixed REST reconciliation loop will still converge the final state.
  if (position.writeReady === false) return position.blockedReason?.trim() || fallback;
  return null;
}

function decimalTone(value: string): "buy" | "sell" | undefined {
  const parts = splitDecimal(value);
  if (!parts || /^0*$/.test(parts.integer + parts.fraction)) return undefined;
  return parts.sign === "-" ? "sell" : "buy";
}
