import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type OnChangeFn,
  type RowData,
  type SortingState,
} from "@tanstack/react-table";
import { useState, type ReactNode } from "react";

import { SortableTableHeader } from "@/components/ui/sortable-table-header";
import { cn } from "@/lib/utils";

declare module "@tanstack/react-table" {
  interface ColumnMeta<TData extends RowData, TValue> {
    /** Numeric columns align right and render mono/tabular. */
    numeric?: boolean;
    align?: "left" | "right";
    /** Fixed width, so a streaming value cannot resize its own column. */
    width?: string;
    /**
     * Pinned to the left edge while the rest of a wide table scrolls under it.
     *
     * Only the anchor column should take this: a sticky cell needs its own
     * background to hide what passes behind it, and a background is what makes
     * it stop sharing the row's hover state.
     */
    sticky?: boolean;
  }
}

/**
 * The background a pinned cell paints, and the hover it has to rejoin.
 *
 * `bg-bg` rather than `bg-panel`: every table in the product sits directly on
 * the shell's canvas, and `.th` carries no background of its own, so an
 * unpainted sticky cell would show the scrolling columns straight through it.
 */
const STICKY = "sticky left-0 z-10 bg-bg";
/**
 * The minimum readable measure for an ordinary data column.
 *
 * Fixed table layout prevents live values from moving columns, but it also
 * needs a width for every otherwise-flexible column. Without one, a narrow
 * viewport gives all remaining columns a few pixels each instead of making the
 * table scroll. Explicit semantic widths still override this value.
 */
const DEFAULT_COLUMN_WIDTH = "128px";

export interface DataTableProps<TData> {
  columns: ColumnDef<TData, unknown>[];
  data: TData[];
  getRowId?: (row: TData, index: number) => string;
  /** Announced to screen readers; not painted. */
  caption: string;
  empty?: ReactNode;
  /** Rows of placeholder to hold the layout while the first page loads. */
  loadingRows?: number;
  loading?: boolean;
  onRowClick?: (row: TData) => void;
  className?: string;
  /** Optional controlled sorting for screens with an explicit sort control. */
  sorting?: SortingState;
  onSortingChange?: OnChangeFn<SortingState>;
  /** Used when the table owns sorting; stable across later renders. */
  initialSorting?: SortingState;
}

export function DataTable<TData>({
  columns,
  data,
  getRowId,
  caption,
  empty,
  loading = false,
  loadingRows = 3,
  onRowClick,
  className,
  sorting: controlledSorting,
  onSortingChange,
  initialSorting = [],
}: DataTableProps<TData>) {
  const [internalSorting, setInternalSorting] = useState<SortingState>(initialSorting);
  const sorting = controlledSorting ?? internalSorting;
  const updateSorting = onSortingChange ?? setInternalSorting;

  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: updateSorting,
    getRowId,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  const headers = table.getHeaderGroups();
  const rows = table.getRowModel().rows;

  if (!loading && rows.length === 0 && empty !== undefined) {
    return <>{empty}</>;
  }

  return (
    <div className={cn("w-full overflow-x-auto", className)}>
      {/* `border-separate` with no spacing rather than `border-collapse`: a
          collapsed border belongs to the table grid, not to the cell, so it does
          not travel with a `position: sticky` column and the pinned cell loses
          its seam the moment the table scrolls. Separated borders are drawn by
          the cells themselves, which is why the header rule below sits on each
          `th` — a `tr` border paints nothing in this mode. */}
      {/* Fixed layout is a correctness property for streaming tables: only the
          column definitions may determine geometry. A longer price, P&L or
          identifier must never make every column move while the operator is
          reading the row. The colgroup makes declared widths authoritative;
          columns without one divide the remaining space predictably. */}
      <table className="w-full table-fixed border-separate border-spacing-0 text-base">
        <caption className="sr-only">{caption}</caption>
        <colgroup>
          {table.getVisibleLeafColumns().map((column) => (
            <col
              key={column.id}
              style={{ width: column.columnDef.meta?.width ?? DEFAULT_COLUMN_WIDTH }}
            />
          ))}
        </colgroup>
        <thead>
          {headers.map((group) => (
            <tr key={group.id}>
              {group.headers.map((header) => {
                const meta = header.column.columnDef.meta;
                const alignRight = meta?.align === "right" || meta?.numeric === true;
                const sortable = header.column.getCanSort();
                const direction = header.column.getIsSorted();
                return (
                  <th
                    key={header.id}
                    scope="col"
                    style={meta?.width ? { width: meta.width } : undefined}
                    aria-sort={
                      !sortable
                        ? undefined
                        : direction === "asc"
                          ? "ascending"
                          : direction === "desc"
                            ? "descending"
                            : "none"
                    }
                    className={cn(
                      "th h-8 border-b border-line px-3",
                      alignRight ? "text-right" : "text-left",
                      meta?.sticky && STICKY,
                    )}
                  >
                    {header.isPlaceholder ? null : sortable ? (
                      <SortableTableHeader
                        direction={direction}
                        onToggle={(event) => header.column.getToggleSortingHandler()?.(event)}
                        align={alignRight ? "right" : "left"}
                      >
                        {flexRender(header.column.columnDef.header, header.getContext())}
                      </SortableTableHeader>
                    ) : (
                      flexRender(header.column.columnDef.header, header.getContext())
                    )}
                  </th>
                );
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {loading && rows.length === 0
            ? Array.from({ length: loadingRows }, (_, index) => (
                <tr key={`skeleton-${index}`} className="h-row">
                  {table.getAllLeafColumns().map((column) => (
                    <td
                      key={column.id}
                      className={cn("px-3", column.columnDef.meta?.sticky && STICKY)}
                    >
                      <span className="block h-3 w-full max-w-24 rounded-seam bg-raised" />
                    </td>
                  ))}
                </tr>
              ))
            : rows.map((row) => (
                <tr
                  key={row.id}
                  tabIndex={onRowClick ? 0 : undefined}
                  onClick={onRowClick ? () => onRowClick(row.original) : undefined}
                  onKeyDown={onRowClick ? (event) => {
                    if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
                    event.preventDefault();
                    onRowClick(row.original);
                  } : undefined}
                  className={cn(
                    "group h-row transition-colors hover:bg-raised",
                    onRowClick && "cursor-pointer",
                  )}
                >
                  {row.getVisibleCells().map((cell) => {
                    const meta = cell.column.columnDef.meta;
                    const alignRight = meta?.align === "right" || meta?.numeric === true;
                    return (
                      <td
                        key={cell.id}
                        {...(meta?.numeric ? { "data-numeric": "" } : {})}
                        className={cn(
                          "truncate px-3 align-middle",
                          alignRight ? "text-right" : "text-left",
                          // Its own background opts the pinned cell out of the
                          // row's hover, so it has to opt back in by hand or the
                          // anchor stays unlit while the row it names highlights.
                          meta?.sticky && `${STICKY} group-hover:bg-raised`,
                        )}
                      >
                        {flexRender(cell.column.columnDef.cell, cell.getContext())}
                      </td>
                    );
                  })}
                </tr>
              ))}
        </tbody>
      </table>
    </div>
  );
}
