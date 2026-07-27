"use client";

import { useCallback, useState } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";

import { Button } from "@/components/ui/button";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { IdText } from "@/components/id-text";
import { formatDateTime } from "@/lib/dates";
import type { AuditEvent } from "@/lib/types";

// One page of an audit log. The backend caps `limit` at 1000 and defaults it to
// 100; a screenful is smaller than either.
export const AUDIT_PAGE_SIZE = 50;

// useAuditPager walks an audit log backwards.
//
// The backend's cursor is exclusive and points at a `seq`, and a page is handed
// back oldest-first, so "older" means "everything below this page's FIRST row".
// The visited cursors are kept as a stack, which is what makes "newer" a pop
// rather than a second, forward-facing query.
export function useAuditPager() {
  const [stack, setStack] = useState<number[]>([]);

  const older = useCallback((events: AuditEvent[] | undefined) => {
    if (!events || events.length === 0) return;
    setStack((s) => [...s, events[0].seq]);
  }, []);
  const newer = useCallback(() => setStack((s) => s.slice(0, -1)), []);
  const reset = useCallback(() => setStack([]), []);

  return {
    // Spread straight into an audit hook.
    query: {
      limit: AUDIT_PAGE_SIZE,
      before: stack.length > 0 ? stack[stack.length - 1] : undefined,
    },
    older,
    newer,
    reset,
    hasNewer: stack.length > 0,
  };
}

export type AuditPager = ReturnType<typeof useAuditPager>;

// PayloadCell renders the event's payload — the snapshot of the entity as it
// was — as pretty-printed JSON behind a disclosure, so the table stays readable
// but nothing in the record is hidden from the reader.
function PayloadCell({ event }: { event: AuditEvent }) {
  if (event.payload === undefined || event.payload === null) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <details className="group">
      <summary className="cursor-pointer list-none text-xs text-muted-foreground hover:text-foreground">
        <span className="group-open:hidden">Show</span>
        <span className="hidden group-open:inline">Hide</span>
      </summary>
      <pre className="mt-2 max-h-64 max-w-md overflow-auto rounded-md bg-muted p-2 text-xs leading-relaxed">
        {JSON.stringify(event.payload, null, 2)}
      </pre>
    </details>
  );
}

const columns: Column<AuditEvent>[] = [
  {
    key: "seq",
    header: "Seq",
    align: "right",
    className: "font-mono text-xs text-muted-foreground",
    render: (e) => e.seq,
  },
  {
    key: "timestamp",
    header: "When",
    render: (e) => formatDateTime(e.timestamp),
  },
  { key: "type", header: "Event", render: (e) => <EnumBadge value={e.type} /> },
  {
    key: "entityId",
    header: "Entity",
    render: (e) => <IdText id={e.entityId} />,
  },
  {
    key: "payload",
    header: "Payload",
    render: (e) => <PayloadCell event={e} />,
  },
];

interface AuditTableProps {
  events: AuditEvent[] | undefined;
  isLoading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  empty?: React.ReactNode;
  pager: AuditPager;
}

// AuditTable is the one rendering of an audit log, shared by all four trails
// (a bank's ledger and deposit logs, the central bank's, and the network's).
// They differ only in which endpoint fills them, so they should not differ in
// how they read.
export function AuditTable({
  events,
  isLoading,
  error,
  onRetry,
  empty,
  pager,
}: AuditTableProps) {
  if (error) {
    return <ErrorState error={error} onRetry={onRetry} />;
  }

  // A full page means there may be older events below it; a short one is the
  // end of the log. The backend gives no total, and counting an unbounded log
  // to render a pager would defeat the point of paging it.
  const mayHaveOlder = (events?.length ?? 0) >= AUDIT_PAGE_SIZE;

  return (
    <div className="space-y-3">
      <DataTable
        columns={columns}
        rows={events}
        rowKey={(e) => e.id}
        isLoading={isLoading}
        empty={empty}
      />
      {(pager.hasNewer || mayHaveOlder) && (
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => pager.older(events)}
            disabled={!mayHaveOlder}
          >
            <ChevronLeft className="size-4" />
            Older
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={pager.newer}
            disabled={!pager.hasNewer}
          >
            Newer
            <ChevronRight className="size-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
