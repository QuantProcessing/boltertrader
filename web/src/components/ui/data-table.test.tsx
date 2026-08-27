import { fireEvent, render, screen } from "@testing-library/react";
import type { ColumnDef } from "@tanstack/react-table";
import { describe, expect, it } from "vitest";

import { DataTable } from "@/components/ui/data-table";

interface Row {
  first: number;
  second: number;
}

const columns: ColumnDef<Row, unknown>[] = [
  { accessorKey: "first", header: "First" },
  { accessorKey: "second", header: "Second" },
];

describe("DataTable sorting", () => {
  it("preserves Shift-click multi-column sorting", () => {
    render(
      <DataTable
        caption="sortable values"
        columns={columns}
        data={[
          { first: 1, second: 2 },
          { first: 1, second: 1 },
        ]}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "First" }));
    fireEvent.click(screen.getByRole("button", { name: "Second" }), { shiftKey: true });

    expect(screen.getByRole("columnheader", { name: "First" })).not.toHaveAttribute("aria-sort", "none");
    expect(screen.getByRole("columnheader", { name: "Second" })).not.toHaveAttribute("aria-sort", "none");
  });

  it("keeps streaming values out of column width calculation", () => {
    const fixedColumns: ColumnDef<Row, unknown>[] = [
      { accessorKey: "first", header: "First", meta: { width: "8rem" } },
      { accessorKey: "second", header: "Second" },
    ];
    const { container, rerender } = render(
      <DataTable
        caption="stable values"
        columns={fixedColumns}
        data={[{ first: 1, second: 2 }]}
      />,
    );

    const table = screen.getByRole("table", { name: "stable values" });
    expect(table).toHaveClass("table-fixed");
    expect(container.querySelectorAll("col")).toHaveLength(2);
    expect(container.querySelector("col")?.style.width).toBe("8rem");
    expect(container.querySelectorAll("col")[1]?.style.width).toBe("128px");

    rerender(
      <DataTable
        caption="stable values"
        columns={fixedColumns}
        data={[{ first: 1, second: Number.MAX_VALUE }]}
      />,
    );
    expect(screen.getByRole("table", { name: "stable values" })).toHaveClass("table-fixed");
    expect(container.querySelector("col")?.style.width).toBe("8rem");
    expect(container.querySelectorAll("col")[1]?.style.width).toBe("128px");
  });
});
