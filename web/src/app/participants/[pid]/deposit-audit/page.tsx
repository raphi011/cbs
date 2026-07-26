"use client";

import { useParams } from "next/navigation";

import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { IdText } from "@/components/id-text";
import { Hint } from "@/components/hint";
import { ErrorState } from "@/components/error-state";
import { useDepositAudit } from "@/lib/api/hooks";
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

export default function DepositAuditPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useDepositAudit(pid);

  return (
    <div className="space-y-4">
      <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
        Deposit audit trail
        <Hint id="audit-trail" />
      </h2>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(e) => e.id}
          isLoading={isLoading}
          empty="No deposit activity yet. Open an account, fund it, or place a hold to populate the log."
        />
      )}
    </div>
  );
}
