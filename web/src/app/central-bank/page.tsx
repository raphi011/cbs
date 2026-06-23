"use client";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { AmountCell } from "@/components/money";
import { CopyId } from "@/components/copy-id";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { useCentralBankAudit, useReserves } from "@/lib/api/hooks";
import { formatDateTime } from "@/lib/dates";
import type { AuditEvent, Reserve } from "@/lib/types";

const reserveColumns: Column<Reserve>[] = [
  {
    key: "participant",
    header: "Participant",
    render: (r) => <CopyId id={r.participant} />,
  },
  {
    key: "reserve",
    header: "Reserve",
    align: "right",
    hint: "reserve-account",
    render: (r) => <AmountCell cents={r.reserve} />,
  },
];

const auditColumns: Column<AuditEvent>[] = [
  {
    key: "timestamp",
    header: "When",
    render: (e) => formatDateTime(e.timestamp),
  },
  {
    key: "type",
    header: "Event",
    render: (e) => <EnumBadge value={e.type} />,
  },
  {
    key: "entityId",
    header: "Entity",
    render: (e) => <CopyId id={e.entityId} />,
  },
];

export default function CentralBankPage() {
  const reserves = useReserves();
  const audit = useCentralBankAudit();

  return (
    <div className="space-y-8">
      <PageHeader
        title="Central bank"
        hint="central-bank-reserves"
        description="Banks meet only here. The central bank holds one reserve account per participant, and settlement is reserves moving between them."
      />

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Reserves</h2>
        {reserves.error ? (
          <ErrorState error={reserves.error} onRetry={() => reserves.refetch()} />
        ) : (
          <DataTable
            columns={reserveColumns}
            rows={reserves.data}
            rowKey={(r) => r.participant}
            isLoading={reserves.isLoading}
            empty="No participants yet. Create one to see its reserve account."
          />
        )}
      </section>

      <section className="space-y-3">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Audit trail
        </h2>
        {audit.error ? (
          <ErrorState error={audit.error} onRetry={() => audit.refetch()} />
        ) : (
          <DataTable
            columns={auditColumns}
            rows={audit.data}
            rowKey={(e) => e.id}
            isLoading={audit.isLoading}
            empty="No central-bank activity yet. Fund a participant or settle a cycle to see reserve movements."
          />
        )}
      </section>
    </div>
  );
}
