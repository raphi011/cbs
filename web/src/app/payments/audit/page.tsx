"use client";

import { useEffect, useState } from "react";

import { Input } from "@/components/ui/input";
import { AuditTable, useAuditPager } from "@/components/audit-table";
import { PageHeader } from "@/components/page-header";
import { usePaymentAudit } from "@/lib/api/hooks";

export default function PaymentAuditPage() {
  const [entity, setEntity] = useState("");
  const pager = useAuditPager();
  const { reset } = pager;

  // A new filter is a different log, so a cursor taken from the old one means
  // nothing: seq is a store-global order, not a position within a filter.
  useEffect(() => {
    reset();
  }, [entity, reset]);

  const { data, isLoading, error, refetch } = usePaymentAudit({
    ...pager.query,
    entity: entity.trim() || undefined,
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Network audit trail"
        hint="audit-trail"
        description="What the network itself did: participants joining, mandates given and revoked, and every payment and clearing cycle as it moved through its lifecycle. A bank's own books keep separate ledger and deposit trails."
      />

      <div className="flex flex-wrap items-center justify-end gap-2">
        <Input
          value={entity}
          onChange={(e) => setEntity(e.target.value)}
          placeholder="Filter by entity ID…"
          className="w-56 font-mono text-xs"
        />
      </div>

      <AuditTable
        events={data}
        isLoading={isLoading}
        error={error}
        onRetry={() => refetch()}
        pager={pager}
        empty={
          entity
            ? "No events for that entity."
            : "No network activity yet. Add a participant, open a cycle, or initiate a payment to populate the log."
        }
      />
    </div>
  );
}
