"use client";

import { useState } from "react";
import { useParams } from "next/navigation";

import { Input } from "@/components/ui/input";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { IdText } from "@/components/id-text";
import { Hint } from "@/components/hint";
import { ErrorState } from "@/components/error-state";
import { useLedgerAudit } from "@/lib/api/hooks";
import { formatDateTime } from "@/lib/dates";
import type { AuditEvent } from "@/lib/types";

const columns: Column<AuditEvent>[] = [
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
];

export default function AuditPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const [entity, setEntity] = useState("");
  const { data, isLoading, error, refetch } = useLedgerAudit(
    pid,
    entity.trim() || undefined,
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Audit trail
          <Hint id="audit-trail" />
        </h2>
        <Input
          value={entity}
          onChange={(e) => setEntity(e.target.value)}
          placeholder="Filter by entity ID…"
          className="w-56 font-mono text-xs"
        />
      </div>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(e) => e.id}
          isLoading={isLoading}
          empty={
            entity
              ? "No events for that entity."
              : "No activity yet. Create accounts or post transactions to populate the log."
          }
        />
      )}
    </div>
  );
}
