import * as Tabs from "@radix-ui/react-tabs";
import type { ColumnDef } from "@tanstack/react-table";
import { Inbox } from "lucide-react";
import { useState } from "react";

import { ApiError } from "@/api/errors";
import type { AccountPosition, AccountPositionState, Command, CommandState, Fill, ManualIntent, PositionFreshness } from "@/api/types";
import { isResumable } from "@/api/types";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { InstrumentName } from "@/components/ui/instrument-name";
import { Num, Side } from "@/components/ui/num";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import { commandFailureReason, venueOfCommand } from "@/features/trade/order-status";
import { liquidationPrice, positionMarkPrice, positionUnrealizedPnl } from "@/features/trade/live-position";
import type { LiveMarkPrice } from "@/features/market/stream";
import { useCancelOrder, useFills, useOrders } from "@/features/trade/queries";
import { compactIdentifier, decimalSign, formatTimestamp } from "@/lib/format";
import { instrumentSymbol } from "@/lib/instrument";
import { useT, type Translate } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The blotter: what is working, what is held, what happened.
 *
 * Three tabs rather than three stacked panels, because the bottom of a terminal
 * is the scarcest space on the screen and an operator looks at exactly one of
 * these at a time. The tab labels carry counts so the two that are not on
 * screen can still say something changed.
 * ------------------------------------------------------------------------- */

export function Blotter({
  instrumentId,
  positions,
  positionsLoading,
  positionsError,
  positionAccounts,
  liveMark,
}: {
  instrumentId: string | null;
  positions: AccountPosition[];
  positionsLoading: boolean;
  positionsError: boolean;
  positionAccounts: AccountPositionState[];
  liveMark?: LiveMarkPrice | null;
}) {
  const orders = useOrders();
  const fills = useFills();

  return (
    <BlotterContent
      instrumentId={instrumentId}
      orders={orders.data ?? []}
      positions={positions}
      fills={fills.data ?? []}
      ordersLoading={orders.isFetching && orders.data === undefined}
      positionsLoading={positionsLoading}
      positionsError={positionsError}
      positionAccounts={positionAccounts}
      liveMark={liveMark}
      fillsLoading={fills.isFetching && fills.data === undefined}
    />
  );
}

/**
 * The anonymous blotter has the same structure but mounts no principal query.
 * Keeping it as a different component type is intentional: signing out
 * unmounts every observer that could still hold the previous session's result.
 */
export function ReadOnlyBlotter({ instrumentId }: { instrumentId: string | null }) {
  return (
    <BlotterContent
      instrumentId={instrumentId}
      orders={[]}
      positions={[]}
      fills={[]}
      ordersLoading={false}
      positionsLoading={false}
      positionsError={false}
      positionAccounts={[]}
      fillsLoading={false}
    />
  );
}

function BlotterContent({
  instrumentId,
  orders,
  positions,
  fills,
  ordersLoading,
  positionsLoading,
  positionsError,
  positionAccounts,
  liveMark,
  fillsLoading,
}: {
  instrumentId: string | null;
  orders: Command[];
  positions: AccountPosition[];
  fills: Fill[];
  ordersLoading: boolean;
  positionsLoading: boolean;
  positionsError: boolean;
  positionAccounts: AccountPositionState[];
  liveMark?: LiveMarkPrice | null;
  fillsLoading: boolean;
}) {
  const t = useT();
  const [activeTab, setActiveTab] = useState("positions");

  return (
    <Tabs.Root value={activeTab} onValueChange={setActiveTab} className="flex min-h-0 flex-1 flex-col">
      {/* `px-0.5` plus the trigger's own `px-2.5` puts the first label on the
          same 12px gutter as the column headers directly beneath it. At `px-2`
          it sat six pixels right of them, which on a deck this short reads as
          the tab bar belonging to something other than the table. */}
      <Tabs.List
        className="flex h-row shrink-0 items-center gap-0.5 border-b border-line px-0.5"
        aria-label={t("blotter.label")}
      >
        <Trigger value="positions" label={t("blotter.positions")} count={positions.length} />
        <Trigger value="orders" label={t("blotter.orders")} count={orders.length} />
        <Trigger value="fills" label={t("blotter.fills")} count={fills.length} />
        {activeTab === "positions" ? (
          <PositionSyncStatus
            loading={positionsLoading}
            error={positionsError}
            accounts={positionAccounts}
          />
        ) : null}
      </Tabs.List>

      <Content value="orders">
        <OrdersTable orders={orders} loading={ordersLoading} />
      </Content>
      <Content value="positions">
        <PositionsTable positions={positions} loading={positionsLoading} liveMark={liveMark} />
      </Content>
      <Content value="fills">
        <FillsTable fills={fills} loading={fillsLoading} instrumentId={instrumentId} />
      </Content>
    </Tabs.Root>
  );
}

function PositionSyncStatus({
  loading,
  error,
  accounts,
}: {
  loading: boolean;
  error: boolean;
  accounts: AccountPositionState[];
}) {
  const t = useT();
  const freshness = worstFreshness(accounts.map((account) => account.freshness));
  if (!error && !loading && (!freshness || freshness === "live")) return null;
  const tone = error || freshness === "unavailable" ? "bg-danger" : loading || freshness === "syncing" ? "bg-pending" : "bg-warn";
  const label = error
    ? t("blotter.positions.syncError")
    : loading
      ? t("blotter.positions.syncing")
      : positionFreshnessLabel(freshness, t);
  return (
    <span className="ml-auto flex items-center gap-1.5 px-3 text-meta text-dim">
      <span className={cn("size-1.5 rounded-full", tone)} aria-hidden="true" />
      {label}
    </span>
  );
}

function Trigger({ value, label, count }: { value: string; label: string; count: number | undefined }) {
  return (
    <Tabs.Trigger
      value={value}
      className={cn(
        "flex h-[26px] items-center gap-1.5 rounded-control px-2.5 text-base text-muted",
        "hover:text-text focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
        "data-[state=active]:bg-raised data-[state=active]:font-medium data-[state=active]:text-text",
      )}
    >
      {label}
      {/* Zero is not news, and a `0` beside every empty tab is three counts to
          read past to find the one that is not zero. */}
      {count ? (
        <span className="rounded-pill bg-fill px-1 text-micro text-dim" data-numeric="">
          {count}
        </span>
      ) : null}
    </Tabs.Trigger>
  );
}

function Content({ value, children }: { value: string; children: React.ReactNode }) {
  return (
    <Tabs.Content value={value} className="min-h-0 flex-1 overflow-y-auto focus-visible:outline-none">
      {children}
    </Tabs.Content>
  );
}

/* --- Orders -------------------------------------------------------------- */

function OrdersTable({ orders, loading }: { orders: Command[]; loading: boolean }) {
  const toast = useToast();
  const cancel = useCancelOrder();
  const t = useT();

  const columns: ColumnDef<Command, unknown>[] = [
    {
      id: "instrument",
      header: t("col.instrument"),
      accessorFn: (order) => intentOf(order)?.instrumentId ?? "—",
      cell: (context) => <InstrumentName instrumentId={String(context.getValue() ?? "")} />,
    },
    {
      id: "state",
      header: t("col.state"),
      accessorFn: (order) => order.state,
      cell: (context) => {
        const order = context.row.original;
        const blockers = order.blockers.map((blocker) => blocker.code).join(", ");
        return (
          // `order.state` and the blocker codes are the server's own words, and
          // stay in them: the audit log and the operator's support ticket both
          // say `uncertain` and `risk.notional_cap` whatever the interface says.
          <StateBadge
            tone={stateTone(order.state)}
            label={order.state}
            detail={blockers === "" ? undefined : t("blotter.blocked", { codes: blockers })}
          />
        );
      },
      meta: { width: "112px" },
    },
    {
      id: "reason",
      header: t("col.reason"),
      accessorFn: (order) => order.state === "failed" ? commandFailureReason(order, venueOfCommand(order), t) : "",
      cell: (context) => {
        const reason = String(context.getValue() ?? "");
        return reason ? <span className="block max-w-72 truncate text-danger" title={reason}>{reason}</span> : <span className="text-dim">—</span>;
      },
      meta: { width: "288px" },
    },
    {
      id: "orderId",
      header: t("col.orderId"),
      accessorFn: (order) => order.commandId,
      cell: (context) => {
        const value = String(context.getValue() ?? "");
        return <span className="text-dim" data-numeric="" title={value}>{compactIdentifier(value, 22)}</span>;
      },
      meta: { width: "156px" },
    },
    {
      id: "side",
      header: t("col.side"),
      accessorFn: (order) => intentOf(order)?.side ?? "",
      cell: (context) => <Side side={String(context.getValue() ?? "")} />,
      meta: { width: "72px" },
    },
    {
      id: "type",
      header: t("col.type"),
      accessorFn: (order) => intentOf(order)?.orderType ?? "—",
      cell: (context) => <span className="capitalize text-muted">{String(context.getValue() ?? "—")}</span>,
      meta: { width: "80px" },
    },
    {
      id: "price",
      header: t("col.price"),
      accessorFn: (order) => intentOf(order)?.limitPrice ?? "",
      cell: (context) => <Num value={asDecimal(context.getValue())} />,
      meta: { numeric: true },
    },
    {
      id: "quantity",
      header: t("col.quantity"),
      accessorFn: (order) => intentOf(order)?.quantity ?? "",
      cell: (context) => <Num value={asDecimal(context.getValue())} />,
      meta: { numeric: true },
    },
    {
      id: "tif",
      header: t("col.tif"),
      accessorFn: (order) => intentOf(order)?.timeInForce ?? "—",
      meta: { width: "64px" },
    },
    {
      id: "updated",
      header: t("col.updated"),
      accessorFn: (order) => order.updatedAt,
      cell: (context) => <span className="text-dim">{formatTimestamp(String(context.getValue() ?? ""))}</span>,
      meta: { align: "right", width: "104px" },
    },
    {
      id: "actions",
      header: "",
      enableSorting: false,
      cell: (context) => {
        const order = context.row.original;
        // Only the states the server will still act on get a button. Offering
        // cancel on a filled order and answering 404 is worse than not
        // offering it.
        if (!isResumable(order.state)) return null;
        return (
          <Button
            variant="danger"
            size="sm"
            disabled={cancel.isPending}
            onClick={() =>
              cancel.mutate(order, {
                onSuccess: () =>
                  toast.push({ tone: "info", title: t("blotter.cancelSubmitted"), detail: compactIdentifier(order.commandId) }),
                onError: (error) =>
                  toast.push({ tone: "danger", title: t("blotter.cancelRejected"), detail: describe(error, t) }),
              })
            }
          >
            {t("blotter.cancel")}
          </Button>
        );
      },
      meta: { align: "right", width: "88px" },
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={orders}
      loading={loading}
      getRowId={(order) => order.commandId}
      caption={t("blotter.caption.orders")}
      empty={
        <EmptyState
          icon={Inbox}
          title={t("blotter.empty.orders")}
          detail={t("blotter.empty.orders.detail")}
        />
      }
      initialSorting={[{ id: "updated", desc: true }]}
    />
  );
}

/* --- Positions ----------------------------------------------------------- */

function PositionsTable({
  positions,
  loading,
  liveMark,
}: {
  positions: AccountPosition[];
  loading: boolean;
  liveMark?: LiveMarkPrice | null;
}) {
  const t = useT();

  const columns: ColumnDef<AccountPosition, unknown>[] = [
    {
      id: "instrument",
      header: t("col.instrument"),
      accessorFn: (position) => position.instrumentId,
      cell: (context) => <InstrumentName instrumentId={String(context.getValue() ?? "")} />,
    },
    {
      id: "side",
      header: t("col.side"),
      accessorFn: (position) => position.side ?? (decimalSign(position.quantity) < 0 ? "sell" : "buy"),
      cell: (context) => <PositionSide side={String(context.getValue() ?? "")} t={t} />,
      meta: { width: "72px" },
    },
    {
      id: "quantity",
      header: t("col.quantity"),
      accessorFn: (position) => position.quantity,
      cell: ({ row }) => <Num value={row.original.quantity.replace(/^-/, "")} suffix={positionQuantityUnit(row.original)} />,
      meta: { numeric: true },
    },
    {
      id: "entry",
      header: t("col.entry"),
      accessorFn: (position) => position.entryPrice,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "mark",
      header: t("col.mark"),
      accessorFn: (position) => positionMarkPrice(position, liveMark?.price, liveMark?.stale).value,
      cell: (context) => <span className={liveMark?.stale ? "text-dim" : undefined}><Num value={String(context.getValue() ?? "")} /></span>,
      meta: { numeric: true },
    },
    {
      id: "unrealized",
      header: t("col.unrealized"),
      accessorFn: (position) => positionUnrealizedPnl(position, liveMark?.price, liveMark?.stale).value,
      cell: ({ row }) => {
        const pnl = positionUnrealizedPnl(row.original, liveMark?.price, liveMark?.stale);
        return <span title={pnl.estimated ? t("portfolio.source.estimated") : undefined}>{pnl.estimated ? "≈ " : ""}<Num value={pnl.value} directional signed /></span>;
      },
      meta: { numeric: true },
    },
    {
      id: "liquidation",
      header: t("portfolio.liquidation"),
      accessorFn: liquidationPrice,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={positions}
      loading={loading}
      getRowId={(position) => `${position.accountId}:${position.routeId ?? ""}:${position.instrumentId}:${position.positionSlot ?? "net"}`}
      caption={t("blotter.caption.positions")}
      empty={
        <EmptyState
          icon={Inbox}
          title={t("blotter.empty.positions")}
          detail={t("blotter.empty.positions.detail")}
        />
      }
    />
  );
}

function worstFreshness(values: PositionFreshness[]): PositionFreshness | undefined {
  return (["unavailable", "stale", "delayed", "syncing", "live"] as PositionFreshness[])
    .find((candidate) => values.includes(candidate));
}

function positionFreshnessLabel(value: PositionFreshness | undefined, t: Translate): string {
  if (value === "live") return t("portfolio.freshness.live");
  if (value === "delayed") return t("portfolio.freshness.delayed");
  if (value === "stale") return t("portfolio.freshness.stale");
  if (value === "syncing") return t("portfolio.freshness.syncing");
  return t("portfolio.freshness.unavailable");
}

function PositionSide({ side, t }: { side: string; t: Translate }) {
  const sell = side.toLowerCase() === "sell" || side.toLowerCase() === "short";
  return <span className={cn("font-medium", sell ? "text-down" : "text-up")}>{t(sell ? "ticket.side.sell" : "ticket.side.buy")}</span>;
}

function positionQuantityUnit(position: AccountPosition): string | undefined {
  const symbol = position.symbol || instrumentSymbol(position.instrumentId);
  const [base, quote] = symbol.toUpperCase().split(/[-_/]/);
  if (!base || !quote) return undefined;
  return ` ${base}`;
}

/* --- Fills --------------------------------------------------------------- */

function FillsTable({
  fills,
  loading,
  instrumentId,
}: {
  fills: Fill[];
  loading: boolean;
  instrumentId: string | null;
}) {
  const t = useT();

  const columns: ColumnDef<Fill, unknown>[] = [
    {
      id: "time",
      header: t("col.time"),
      accessorFn: (fill) => fill.occurredAt,
      cell: (context) => formatTimestamp(String(context.getValue() ?? "")),
      meta: { width: "104px" },
    },
    {
      id: "instrument",
      header: t("col.instrument"),
      accessorFn: (fill) => fill.instrumentId,
      cell: (context) => <InstrumentName instrumentId={String(context.getValue() ?? "")} />,
    },
    {
      id: "side",
      header: t("col.side"),
      accessorFn: (fill) => fill.side,
      cell: (context) => <Side side={String(context.getValue() ?? "")} />,
      meta: { width: "72px" },
    },
    {
      id: "price",
      header: t("col.price"),
      accessorFn: (fill) => fill.price,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "quantity",
      header: t("col.quantity"),
      accessorFn: (fill) => fill.quantity,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "fee",
      header: t("col.fee"),
      accessorFn: (fill) => fill.fee,
      cell: (context) => (
        <span>
          <Num value={String(context.getValue() ?? "")} />{" "}
          <span className="text-dim">{context.row.original.feeCurrency}</span>
        </span>
      ),
      meta: { numeric: true },
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={fills}
      loading={loading}
      getRowId={(fill) => fill.fillId}
      caption={t("blotter.caption.fills")}
      empty={
        <EmptyState
          icon={Inbox}
          title={t("blotter.empty.fills")}
          // No instrument selected means no name to put in the sentence, and
          // "nothing has executed on  or anywhere else" is worse than silence.
          detail={instrumentId ? t("blotter.empty.fills.detail", { instrument: instrumentSymbol(instrumentId) }) : undefined}
        />
      }
    />
  );
}

/* --- Helpers ------------------------------------------------------------- */

/**
 * `Command.payload` is opaque JSON on the wire. Every command the manual
 * product writes carries a `ManualIntent`, but the type says `unknown` and a
 * blotter that trusts that would crash on the first record written by anything
 * else. Narrow, and render an em dash when it is not what we expected.
 */
function intentOf(command: Command): ManualIntent | null {
  const payload = command.payload;
  if (typeof payload !== "object" || payload === null) return null;
  return "instrumentId" in payload ? (payload as ManualIntent) : null;
}

/** Table accessors are typed `unknown`; decimals reach `Num` as strings or not at all. */
function asDecimal(value: unknown): string | null {
  return typeof value === "string" && value !== "" ? value : null;
}

/**
 * `uncertain` is deliberately `warn` rather than `danger`: the venue was asked
 * and did not answer, which is a thing to watch rather than a thing that failed.
 */
function stateTone(state: CommandState): StateTone {
  switch (state) {
    case "filled":
      return "ok";
    case "accepted":
    case "submitted":
      return "pending";
    case "uncertain":
      return "warn";
    case "failed":
      return "danger";
    case "canceled":
      return "neutral";
  }
}

/**
 * Outside React, so it takes `t` rather than calling `useT()`. Both branches
 * above it are the server's own text; only the "we have no idea" fallback is
 * ours to translate.
 */
function describe(error: unknown, t: Translate): string {
  if (error instanceof ApiError) return `${error.message} (${error.code})`;
  return error instanceof Error ? error.message : t("error.unknown");
}
