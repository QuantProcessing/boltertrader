import type { ColumnDef } from "@tanstack/react-table";
import { Inbox, Wallet } from "lucide-react";
import { useMemo, useState } from "react";

import type { LedgerEntry, Position } from "@/api/types";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { Num } from "@/components/ui/num";
import { Metric } from "@/components/ui/panel";
import { Tag } from "@/components/ui/state-badge";
import { useAccounts } from "@/features/market/queries";
import { pnlKeyOf, useLedger, usePnL, usePortfolioPositions } from "@/features/portfolio/queries";
import { decimalSign, formatDecimal, formatTimestamp } from "@/lib/format";
import { useT, type Translate } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * Portfolio: what is held, and what it earned.
 *
 * Selecting a position is not a convenience here, it is the query. P&L is
 * attributed per lineage per instrument and the server refuses anything less
 * specific, so the strip at the top is the breakdown for the row below that the
 * operator pointed at — never a total, because a total across settlement
 * currencies is a number with no unit and the ledger service says so out loud.
 *
 * Same three-band layout as the terminal — strip, primary table, record
 * beneath — so the two pages read the same way.
 * ------------------------------------------------------------------------- */

/** Stable across refetches: a lineage holds one instrument at a time. */
const rowIdOf = (position: Position): string => `${position.lineageId}:${position.instrumentId}`;

export function PortfolioPage() {
  const t = useT();
  const accounts = useAccounts();
  const [accountId, setAccountId] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const positions = usePortfolioPositions(useMemo(() => (accountId ? { accountId } : {}), [accountId]));
  const rows = positions.data ?? [];

  // Resolved from the live list rather than held as a snapshot, so a row that
  // closes while it is selected stops being selected instead of freezing a
  // stale quantity on screen.
  const selected = rows.find((position) => rowIdOf(position) === selectedId) ?? null;

  const pnl = usePnL(selected ? pnlKeyOf(selected) : null);

  // Without a selection the ledger is everything in scope; with one it narrows
  // to the same lineage the strip is describing.
  const ledger = useLedger(
    useMemo(
      () => (selected ? pnlKeyOf(selected) : accountId ? { accountId } : {}),
      [selected, accountId],
    ),
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-bar shrink-0 items-center gap-4 border-b border-line px-3">
        {selected ? (
          <>
            <div className="min-w-0">
              <div className="th truncate">{selected.instrumentId}</div>
              <div className="mt-1 truncate text-meta text-dim">
                {selected.accountId} · {selected.productId}
              </div>
            </div>
            <div className="flex min-w-0 flex-1 items-center gap-6 overflow-x-auto">
              <Money label={t("portfolio.realized")} value={pnl.data?.realizedPnl} />
              <Money label={t("portfolio.unrealized")} value={pnl.data?.unrealizedPnl} />
              <Money label={t("portfolio.fees")} value={pnl.data?.fees} />
              <Money label={t("portfolio.funding")} value={pnl.data?.funding} />
              <Money label={t("portfolio.net")} value={pnl.data?.netPnl} unit={selected.settlementCurrency} />
            </div>
          </>
        ) : (
          <p className="min-w-0 flex-1 truncate text-meta text-dim">{t("portfolio.hint")}</p>
        )}

        <label className="flex shrink-0 items-center gap-2">
          <span className="th">{t("portfolio.account")}</span>
          <select
            value={accountId}
            onChange={(event) => {
              setAccountId(event.target.value);
              setSelectedId(null);
            }}
            className={cn(inputClass, "w-48")}
          >
            <option value="">{t("filter.allAccounts")}</option>
            {(accounts.data ?? []).map((account) => (
              <option key={account.accountId} value={account.accountId}>
                {account.displayName}
              </option>
            ))}
          </select>
        </label>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <DataTable
          columns={positionColumns(selectedId, t)}
          data={rows}
          getRowId={rowIdOf}
          caption={t("portfolio.caption.positions")}
          loading={positions.isPending}
          onRowClick={(position) => {
            const id = rowIdOf(position);
            setSelectedId((current) => (current === id ? null : id));
          }}
          empty={
            <EmptyState
              icon={Wallet}
              title={t("portfolio.empty.positions")}
              detail={t("portfolio.empty.positions.detail")}
            />
          }
        />
      </div>

      <section className="flex h-deck shrink-0 flex-col border-t border-line">
        <header className="flex h-row shrink-0 items-center gap-2 border-b border-line px-3">
          <h2 className="text-base font-medium text-text">{t("portfolio.ledger")}</h2>
          {selected ? <Tag>{t("portfolio.thisLineage")}</Tag> : null}
          <span className="ml-auto text-meta text-dim">
            {t("portfolio.entries", { count: ledger.data?.length ?? 0 })}
          </span>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <DataTable
            columns={ledgerColumns(t)}
            data={ledger.data ?? []}
            getRowId={(entry) => entry.entryId}
            caption={t("portfolio.caption.ledger")}
            loading={ledger.isPending}
            empty={
              <EmptyState
                icon={Inbox}
                title={t("portfolio.empty.ledger")}
                detail={t("portfolio.empty.ledger.detail")}
              />
            }
          />
        </div>
      </section>
    </div>
  );
}

/** One figure in the strip. Blank rather than zero while the P&L is loading. */
function Money({ label, value, unit }: { label: string; value: string | undefined; unit?: string }) {
  const sign = decimalSign(value);
  return (
    <Metric
      label={label}
      value={value === undefined ? "—" : formatDecimal(value, { decimals: 2, signed: true, grouped: true })}
      unit={unit}
      tone={sign > 0 ? "up" : sign < 0 ? "down" : "none"}
      className="shrink-0"
    />
  );
}

function positionColumns(selectedId: string | null, t: Translate): ColumnDef<Position, unknown>[] {
  return [
    {
      id: "instrument",
      header: t("col.instrument"),
      accessorKey: "instrumentId",
      cell: ({ row }) => (
        <span className="flex items-center gap-2">
          <span
            aria-hidden="true"
            className={cn(
              "h-3.5 w-0.5 shrink-0 rounded-full",
              rowIdOf(row.original) === selectedId ? "bg-accent" : "bg-transparent",
            )}
          />
          <span className="truncate">{row.original.instrumentId}</span>
        </span>
      ),
    },
    { id: "account", header: t("col.account"), accessorKey: "accountId", meta: { width: "12rem" } },
    { id: "product", header: t("col.product"), accessorKey: "productId", meta: { width: "6rem" } },
    {
      id: "quantity",
      header: t("col.quantity"),
      accessorKey: "quantity",
      meta: { numeric: true },
      cell: ({ row }) => <Num value={row.original.quantity} signed directional />,
    },
    {
      id: "entry",
      header: t("col.entry"),
      accessorKey: "averageEntryPrice",
      meta: { numeric: true },
      cell: ({ row }) => <Num value={row.original.averageEntryPrice} />,
    },
    {
      id: "mark",
      header: t("col.mark"),
      accessorKey: "markPrice",
      meta: { numeric: true },
      cell: ({ row }) => <Num value={row.original.markPrice} />,
    },
    {
      id: "unrealized",
      header: t("col.unrealized"),
      accessorKey: "unrealizedPnl",
      meta: { numeric: true },
      cell: ({ row }) => <Num value={row.original.unrealizedPnl} decimals={2} signed grouped directional />,
    },
    {
      id: "realized",
      header: t("col.realized"),
      accessorKey: "realizedPnl",
      meta: { numeric: true },
      cell: ({ row }) => <Num value={row.original.realizedPnl} decimals={2} signed grouped directional />,
    },
    {
      id: "currency",
      header: t("col.currency"),
      accessorKey: "settlementCurrency",
      meta: { width: "5rem" },
    },
    {
      id: "updated",
      header: t("col.updated"),
      accessorKey: "updatedAt",
      meta: { align: "right", width: "7rem" },
      cell: ({ row }) => <span className="text-dim">{formatTimestamp(row.original.updatedAt)}</span>,
    },
  ];
}

/**
 * A builder rather than a constant, now that the headers are translated: a
 * module-level array is evaluated once at import and would freeze whichever
 * locale happened to be active then.
 *
 * `kind` is the server's own vocabulary (`command.submitted`, `fee`) and is not
 * translated — the activity log and the audit trail print the same token.
 */
function ledgerColumns(t: Translate): ColumnDef<LedgerEntry, unknown>[] {
  return [
    {
      id: "occurred",
      header: t("col.time"),
      accessorKey: "occurredAt",
      meta: { width: "7rem" },
      cell: ({ row }) => <span className="text-dim">{formatTimestamp(row.original.occurredAt)}</span>,
    },
    { id: "kind", header: t("col.kind"), accessorKey: "kind", meta: { width: "9rem" } },
    { id: "instrument", header: t("col.instrument"), accessorKey: "instrumentId" },
    { id: "account", header: t("col.account"), accessorKey: "accountId", meta: { width: "12rem" } },
    {
      id: "amount",
      header: t("col.amount"),
      accessorKey: "amount",
      meta: { numeric: true },
      cell: ({ row }) => <Num value={row.original.amount} decimals={2} signed grouped directional />,
    },
    { id: "currency", header: t("col.currency"), accessorKey: "currency", meta: { width: "5rem" } },
  ];
}
