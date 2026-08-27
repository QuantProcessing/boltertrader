import type { ColumnDef } from "@tanstack/react-table";
import { Activity } from "lucide-react";
import { useMemo, useState } from "react";

import type { ActivityEvent } from "@/api/types";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { Tag } from "@/components/ui/state-badge";
import { useActivity } from "@/features/activity/queries";
import { useAccounts } from "@/features/market/queries";
import { formatTimestamp } from "@/lib/format";
import { useT, type Translate } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * Activity: the ordered record of what the system did.
 *
 * The log is read whole and filtered here rather than by the server. Trading
 * holds it in a 1024-event ring, so the whole thing is one small response — and
 * filtering locally is what lets the kind list be built from the kinds that
 * actually occurred. The vocabulary is not published anywhere the browser can
 * read, so a server-side kind filter would mean typing a string and hoping.
 *
 * `productId`, `accountId` and `resourceId` are nullable on the wire and are
 * rendered as an em dash. Blank would be indistinguishable from an event whose
 * scope simply is not known, which is a different fact.
 * ------------------------------------------------------------------------- */

/** How many rows are painted at once. Beyond this the page says what it hid. */
const RENDER_LIMIT = 300;

export function ActivityPage() {
  const t = useT();
  const accounts = useAccounts();
  const events = useActivity(useMemo(() => ({}), []));
  const [kind, setKind] = useState("");
  const [accountId, setAccountId] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const all = events.data ?? [];

  const kinds = useMemo(() => [...new Set(all.map((event) => event.kind))].sort(), [all]);

  const matched = useMemo(
    () =>
      all.filter(
        (event) =>
          (kind === "" || event.kind === kind) && (accountId === "" || event.accountId === accountId),
      ),
    [all, kind, accountId],
  );

  const rows = matched.slice(0, RENDER_LIMIT);
  const selected = all.find((event) => event.eventId === selectedId) ?? null;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-bar shrink-0 items-center gap-4 border-b border-line px-3">
        <label className="flex shrink-0 items-center gap-2">
          <span className="th">{t("col.kind")}</span>
          <select
            value={kind}
            onChange={(event) => setKind(event.target.value)}
            className={cn(inputClass, "w-56")}
          >
            <option value="">{t("filter.allKinds")}</option>
            {/* The kinds are the server's own tokens and are never translated:
                the filter, the cell and the audit trail must say the same word. */}
            {kinds.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>

        <label className="flex shrink-0 items-center gap-2">
          <span className="th">{t("col.account")}</span>
          <select
            value={accountId}
            onChange={(event) => setAccountId(event.target.value)}
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

        <p className="ml-auto shrink-0 text-meta text-dim">
          {t("activity.count", { matched: matched.length, total: all.length })}
          {matched.length > rows.length
            ? ` · ${t("activity.newest", { count: rows.length })}`
            : ""}
        </p>
      </header>

      <div className="flex min-h-0 flex-1">
        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto">
          <DataTable
            columns={columns(selectedId, t)}
            data={rows}
            getRowId={(event) => event.eventId}
            caption={t("activity.caption")}
            loading={events.isPending}
            onRowClick={(event) =>
              setSelectedId((current) => (current === event.eventId ? null : event.eventId))
            }
            empty={
              <EmptyState
                icon={Activity}
                title={
                  all.length === 0 ? t("activity.empty.none") : t("activity.empty.filtered")
                }
                detail={all.length === 0 ? t("activity.empty.none.detail") : undefined}
              />
            }
          />
        </div>

        <aside className="w-96 shrink-0 overflow-y-auto border-l border-line">
          <Detail event={selected} />
        </aside>
      </div>
    </div>
  );
}

function Detail({ event }: { event: ActivityEvent | null }) {
  const t = useT();

  if (!event) {
    return (
      <EmptyState
        title={t("activity.noSelection")}
        detail={t("activity.noSelection.detail")}
      />
    );
  }
  return (
    <div className="flex flex-col">
      <header className="flex h-row shrink-0 items-center gap-2 border-b border-line px-3">
        {/* The kind and the sequence number are the record itself. */}
        <h2 className="truncate text-base font-medium text-text">{event.kind}</h2>
        <Tag className="ml-auto">#{event.sequence}</Tag>
      </header>

      <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-2 border-b border-line p-3 text-meta">
        <Row label={t("activity.observed")} value={formatTimestamp(event.observedAt)} />
        <Row label={t("col.product")} value={event.productId} />
        <Row label={t("col.account")} value={event.accountId} />
        <Row label={t("col.resource")} value={event.resourceId} />
      </dl>

      <div className="p-3">
        <div className="th mb-2">{t("activity.payload")}</div>
        <pre
          data-numeric=""
          className="overflow-x-auto rounded-card bg-fill p-2 text-meta text-muted"
        >
          {JSON.stringify(event.payload, null, 2)}
        </pre>
      </div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string | null }) {
  return (
    <>
      <dt className="th">{label}</dt>
      <dd className={cn("truncate", value === null ? "text-dim" : "text-muted")}>{value ?? "—"}</dd>
    </>
  );
}

/**
 * A builder rather than a constant, now that the headers are translated: a
 * module-level array is evaluated once at import and would freeze whichever
 * locale happened to be active then.
 */
function columns(selectedId: string | null, t: Translate): ColumnDef<ActivityEvent, unknown>[] {
  return [
    {
      id: "observed",
      header: t("col.time"),
      accessorKey: "observedAt",
      meta: { width: "7rem" },
      cell: ({ row }) => (
        <span className="flex items-center gap-2">
          <span
            aria-hidden="true"
            className={cn(
              "h-3.5 w-0.5 shrink-0 rounded-full",
              row.original.eventId === selectedId ? "bg-accent" : "bg-transparent",
            )}
          />
          <span className="text-dim">{formatTimestamp(row.original.observedAt)}</span>
        </span>
      ),
    },
    { id: "kind", header: t("col.kind"), accessorKey: "kind", meta: { width: "16rem" } },
    {
      id: "product",
      header: t("col.product"),
      accessorKey: "productId",
      meta: { width: "7rem" },
      cell: ({ row }) => <Scope value={row.original.productId} />,
    },
    {
      id: "account",
      header: t("col.account"),
      accessorKey: "accountId",
      meta: { width: "12rem" },
      cell: ({ row }) => <Scope value={row.original.accountId} />,
    },
    {
      id: "resource",
      header: t("col.resource"),
      accessorKey: "resourceId",
      cell: ({ row }) => <Scope value={row.original.resourceId} />,
    },
    {
      id: "sequence",
      header: t("col.seq"),
      accessorKey: "sequence",
      meta: { numeric: true, width: "5rem" },
    },
  ];
}

/** Null is a fact about the event, so it gets a mark of its own. */
function Scope({ value }: { value: string | null }) {
  return value === null ? <span className="text-dim">—</span> : <span className="truncate">{value}</span>;
}
