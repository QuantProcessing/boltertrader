import * as Tabs from "@radix-ui/react-tabs";
import type { ColumnDef } from "@tanstack/react-table";
import { Inbox } from "lucide-react";

import { ApiError } from "@/api/errors";
import type { Command, CommandState, Fill, ManualIntent, Position } from "@/api/types";
import { isResumable } from "@/api/types";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { Num, Side } from "@/components/ui/num";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import { useCancelOrder, useFills, useOrders, usePositions } from "@/features/trade/queries";
import { formatTimestamp } from "@/lib/format";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The blotter: what is working, what is held, what happened.
 *
 * Three tabs rather than three stacked panels, because the bottom of a terminal
 * is the scarcest space on the screen and an operator looks at exactly one of
 * these at a time. The tab labels carry counts so the two that are not on
 * screen can still say something changed.
 * ------------------------------------------------------------------------- */

export function Blotter({ instrumentId }: { instrumentId: string | null }) {
  const orders = useOrders();
  const positions = usePositions();
  const fills = useFills();

  // Working orders are the reason to look down here, so the count that decides
  // the default tab is the actionable one, not the total.
  const working = (orders.data ?? []).filter((order) => isResumable(order.state));

  return (
    <Tabs.Root defaultValue="orders" className="flex min-h-0 flex-1 flex-col">
      <Tabs.List
        className="flex h-row shrink-0 items-center gap-1 border-b border-line px-2"
        aria-label="Blotter"
      >
        <Trigger value="orders" label="Orders" count={working.length} />
        <Trigger value="positions" label="Positions" count={positions.data?.length} />
        <Trigger value="fills" label="Fills" count={fills.data?.length} />
      </Tabs.List>

      <Content value="orders">
        <OrdersTable orders={orders.data ?? []} loading={orders.isPending} />
      </Content>
      <Content value="positions">
        <PositionsTable positions={positions.data ?? []} loading={positions.isPending} />
      </Content>
      <Content value="fills">
        <FillsTable fills={fills.data ?? []} loading={fills.isPending} instrumentId={instrumentId} />
      </Content>
    </Tabs.Root>
  );
}

function Trigger({ value, label, count }: { value: string; label: string; count: number | undefined }) {
  return (
    <Tabs.Trigger
      value={value}
      className={cn(
        "flex h-[26px] items-center gap-1.5 rounded-control px-2.5 text-base text-muted",
        "hover:text-text focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
        "data-[state=active]:bg-raised data-[state=active]:text-text",
      )}
    >
      {label}
      {count ? <span className="text-meta text-dim">{count}</span> : null}
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

  const columns: ColumnDef<Command, unknown>[] = [
    {
      id: "instrument",
      header: "Instrument",
      accessorFn: (order) => intentOf(order)?.instrumentId ?? "—",
      cell: (context) => <span className="text-text">{String(context.getValue() ?? "—")}</span>,
    },
    {
      id: "side",
      header: "Side",
      accessorFn: (order) => intentOf(order)?.side ?? "",
      cell: (context) => <Side side={String(context.getValue() ?? "")} />,
      meta: { width: "72px" },
    },
    {
      id: "type",
      header: "Type",
      accessorFn: (order) => intentOf(order)?.orderType ?? "—",
      cell: (context) => <span className="capitalize text-muted">{String(context.getValue() ?? "—")}</span>,
      meta: { width: "80px" },
    },
    {
      id: "price",
      header: "Price",
      accessorFn: (order) => intentOf(order)?.limitPrice ?? "",
      cell: (context) => <Num value={asDecimal(context.getValue())} />,
      meta: { numeric: true },
    },
    {
      id: "quantity",
      header: "Quantity",
      accessorFn: (order) => intentOf(order)?.quantity ?? "",
      cell: (context) => <Num value={asDecimal(context.getValue())} />,
      meta: { numeric: true },
    },
    {
      id: "tif",
      header: "TIF",
      accessorFn: (order) => intentOf(order)?.timeInForce ?? "—",
      meta: { width: "64px" },
    },
    {
      id: "state",
      header: "State",
      accessorFn: (order) => order.state,
      cell: (context) => {
        const order = context.row.original;
        const blockers = order.blockers.map((blocker) => blocker.code).join(", ");
        return (
          <StateBadge
            tone={stateTone(order.state)}
            label={order.state}
            detail={blockers === "" ? undefined : `Blocked: ${blockers}`}
          />
        );
      },
      meta: { width: "112px" },
    },
    {
      id: "updated",
      header: "Updated",
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
                onSuccess: () => toast.push({ tone: "info", title: "Cancel submitted", detail: order.commandId }),
                onError: (error) =>
                  toast.push({ tone: "danger", title: "Cancel rejected", detail: describe(error) }),
              })
            }
          >
            Cancel
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
      caption="Manual orders"
      empty={<EmptyState icon={Inbox} title="No orders yet" detail="Placed orders appear here as soon as the venue acknowledges them." />}
    />
  );
}

/* --- Positions ----------------------------------------------------------- */

function PositionsTable({ positions, loading }: { positions: Position[]; loading: boolean }) {
  const columns: ColumnDef<Position, unknown>[] = [
    { id: "instrument", header: "Instrument", accessorFn: (position) => position.instrumentId },
    {
      id: "quantity",
      header: "Quantity",
      accessorFn: (position) => position.quantity,
      cell: (context) => <Num value={String(context.getValue() ?? "")} directional />,
      meta: { numeric: true },
    },
    {
      id: "entry",
      header: "Avg entry",
      accessorFn: (position) => position.averageEntryPrice,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "mark",
      header: "Mark",
      accessorFn: (position) => position.markPrice,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "unrealized",
      header: "Unrealized",
      accessorFn: (position) => position.unrealizedPnl,
      cell: (context) => <Num value={String(context.getValue() ?? "")} directional signed />,
      meta: { numeric: true },
    },
    {
      id: "realized",
      header: "Realized",
      accessorFn: (position) => position.realizedPnl,
      cell: (context) => <Num value={String(context.getValue() ?? "")} directional signed />,
      meta: { numeric: true },
    },
    {
      id: "currency",
      header: "Settles",
      accessorFn: (position) => position.settlementCurrency,
      meta: { width: "80px" },
    },
    {
      id: "updated",
      header: "Updated",
      accessorFn: (position) => position.updatedAt,
      cell: (context) => <span className="text-dim">{formatTimestamp(String(context.getValue() ?? ""))}</span>,
      meta: { align: "right", width: "104px" },
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={positions}
      loading={loading}
      getRowId={(position) => `${position.lineageId}:${position.instrumentId}`}
      caption="Open positions"
      empty={<EmptyState icon={Inbox} title="Flat" detail="No open position on any instrument." />}
    />
  );
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
  const columns: ColumnDef<Fill, unknown>[] = [
    {
      id: "time",
      header: "Time",
      accessorFn: (fill) => fill.occurredAt,
      cell: (context) => formatTimestamp(String(context.getValue() ?? "")),
      meta: { width: "104px" },
    },
    { id: "instrument", header: "Instrument", accessorFn: (fill) => fill.instrumentId },
    {
      id: "side",
      header: "Side",
      accessorFn: (fill) => fill.side,
      cell: (context) => <Side side={String(context.getValue() ?? "")} />,
      meta: { width: "72px" },
    },
    {
      id: "price",
      header: "Price",
      accessorFn: (fill) => fill.price,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "quantity",
      header: "Quantity",
      accessorFn: (fill) => fill.quantity,
      cell: (context) => <Num value={String(context.getValue() ?? "")} />,
      meta: { numeric: true },
    },
    {
      id: "fee",
      header: "Fee",
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
      caption="Recent fills"
      empty={
        <EmptyState
          icon={Inbox}
          title="No fills"
          detail={instrumentId ? `Nothing has executed on ${instrumentId} or anywhere else.` : undefined}
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

function describe(error: unknown): string {
  if (error instanceof ApiError) return `${error.message} (${error.code})`;
  return error instanceof Error ? error.message : "Unknown failure";
}
