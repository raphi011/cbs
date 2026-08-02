"use client";

import { PageHeader } from "@/components/page-header";
import { AuditTable, useAuditPager } from "@/components/audit-table";
import { useCentralBankAudit } from "@/lib/api/hooks";

// The central bank's own log: reserve movements and settlements, the events
// produced by the layer the banks meet at. A bank's ledger and deposit logs are
// its own and live in its back office — GET /audit means "this operator's own
// log" on every operator, which is the split being consistent rather than
// colliding.
export default function CentralBankAuditPage() {
  const pager = useAuditPager();
  const audit = useCentralBankAudit(pager.query);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Audit"
        hint="audit-trail"
        description="Every mutation the central-bank layer produced, append-only and in order."
      />
      <AuditTable
        events={audit.data}
        isLoading={audit.isLoading}
        error={audit.error}
        onRetry={() => audit.refetch()}
        pager={pager}
        empty="No central-bank activity yet. Fund a participant, or close a clearing cycle so this bank is instructed to discharge it, to see reserve movements."
      />
    </div>
  );
}
