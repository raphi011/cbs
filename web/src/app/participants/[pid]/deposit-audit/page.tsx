"use client";

import { useParams } from "next/navigation";

import { AuditTable, useAuditPager } from "@/components/audit-table";
import { Hint } from "@/components/hint";
import { useDepositAudit } from "@/lib/api/hooks";

export default function DepositAuditPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const pager = useAuditPager();
  const { data, isLoading, error, refetch } = useDepositAudit(pid, pager.query);

  return (
    <div className="space-y-4">
      <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
        Deposit audit trail
        <Hint id="audit-trail" />
      </h2>

      <AuditTable
        events={data}
        isLoading={isLoading}
        error={error}
        onRetry={() => refetch()}
        pager={pager}
        empty="No deposit activity yet. Open an account, fund it, or place a hold to populate the log."
      />
    </div>
  );
}
