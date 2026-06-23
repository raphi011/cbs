"use client";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { Hint } from "./hint";
import type { HintKey } from "./hint-content";

export interface Column<T> {
  // Stable key for the column.
  key: string;
  header: React.ReactNode;
  // Optional "?" hint on the column header.
  hint?: HintKey;
  align?: "left" | "right" | "center";
  // Cell renderer; defaults to indexing the row by `key`.
  render?: (row: T) => React.ReactNode;
  className?: string;
}

interface DataTableProps<T> {
  columns: Column<T>[];
  rows: T[] | undefined;
  rowKey: (row: T) => string;
  isLoading?: boolean;
  empty?: React.ReactNode;
  onRowClick?: (row: T) => void;
}

const alignClass = {
  left: "text-left",
  right: "text-right",
  center: "text-center",
} as const;

// A thin, typed wrapper over the shadcn Table with built-in loading and empty
// states and optional per-column "?" hints — used by every list screen.
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  isLoading,
  empty = "Nothing here yet.",
  onRowClick,
}: DataTableProps<T>) {
  return (
    <div className="overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            {columns.map((col) => (
              <TableHead
                key={col.key}
                className={cn(col.align && alignClass[col.align])}
              >
                <span className="inline-flex items-center gap-1.5">
                  {col.header}
                  {col.hint && <Hint id={col.hint} />}
                </span>
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {isLoading ? (
            Array.from({ length: 3 }).map((_, i) => (
              <TableRow key={`skeleton-${i}`}>
                {columns.map((col) => (
                  <TableCell key={col.key}>
                    <Skeleton className="h-4 w-full" />
                  </TableCell>
                ))}
              </TableRow>
            ))
          ) : !rows || rows.length === 0 ? (
            <TableRow>
              <TableCell
                colSpan={columns.length}
                className="py-10 text-center text-sm text-muted-foreground"
              >
                {empty}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow
                key={rowKey(row)}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                className={cn(onRowClick && "cursor-pointer")}
              >
                {columns.map((col) => (
                  <TableCell
                    key={col.key}
                    className={cn(
                      col.align && alignClass[col.align],
                      col.className,
                    )}
                  >
                    {col.render
                      ? col.render(row)
                      : String((row as Record<string, unknown>)[col.key] ?? "")}
                  </TableCell>
                ))}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  );
}
