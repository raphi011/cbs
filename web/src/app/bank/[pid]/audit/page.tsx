"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";

import { Input } from "@/components/ui/input";
import { AuditTable, useAuditPager } from "@/components/audit-table";
import { Hint } from "@/components/hint";
import { useLedgerAudit } from "@/lib/api/hooks";

export default function AuditPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const [entity, setEntity] = useState("");
  const pager = useAuditPager();
  const { reset } = pager;

  // A new filter is a different log, so a cursor taken from the old one means
  // nothing: seq is a store-global order, not a position within a filter.
  useEffect(() => {
    reset();
  }, [entity, reset]);

  const { data, isLoading, error, refetch } = useLedgerAudit(pid, {
    ...pager.query,
    entity: entity.trim() || undefined,
  });

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

      <AuditTable
        events={data}
        isLoading={isLoading}
        error={error}
        onRetry={() => refetch()}
        pager={pager}
        empty={
          entity
            ? "No events for that entity."
            : "No activity yet. Create accounts or post transactions to populate the log."
        }
      />
    </div>
  );
}
