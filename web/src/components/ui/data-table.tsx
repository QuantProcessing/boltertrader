import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type RowData,
  type SortingState,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp } from "lucide-react";
import { useState, type ReactNode } from "react";

import { cn } from "@/lib/utils";

declare module "@tanstack/react-table" {
  interface ColumnMeta<TData extends RowData, TValue> {
    /** Numeric columns align right and render mono/tabular. */
    numeric?: boolean;
    align?: "left" | "right";
    /** Fixed width, so a streaming value cannot resize its own column. */
    width?: string;
  }
}

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
}: DataTableProps<TData>) {
  const [sorting, setSorting] = useState<SortingState>([]);

  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
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
      <table className="w-full border-collapse text-base">
        <caption className="sr-only">{caption}</caption>
        <thead>
          {headers.map((group) => (
            <tr key={group.id} className="border-b border-line">
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
                    className={cn("th h-8 px-3", alignRight ? "text-right" : "text-left")}
                  >
                    {header.isPlaceholder ? null : sortable ? (
                      <button
                        type="button"
                        onClick={header.column.getToggleSortingHandler()}
                        className={cn(
                          // `uppercase` is repeated from `.th` on purpose: the UA
                          // stylesheet resets text-transform on form controls, so
                          // without it a sortable column renders sentence case and
                          // an unsortable one beside it renders caps.
                          "inline-flex items-center gap-1 rounded-control uppercase hover:text-text",
                          alignRight && "flex-row-reverse",
                        )}
                      >
                        {flexRender(header.column.columnDef.header, header.getContext())}
                        {direction === "asc" ? (
                          <ArrowUp className="size-3" aria-hidden="true" />
                        ) : direction === "desc" ? (
                          <ArrowDown className="size-3" aria-hidden="true" />
                        ) : null}
                      </button>
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
                    <td key={column.id} className="px-3">
                      <span className="block h-3 w-full max-w-24 rounded-seam bg-raised" />
                    </td>
                  ))}
                </tr>
              ))
            : rows.map((row) => (
                <tr
                  key={row.id}
                  onClick={onRowClick ? () => onRowClick(row.original) : undefined}
                  className={cn(
                    "h-row transition-colors hover:bg-raised",
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
