"use client";

import { PageHeader } from "@/components/page-header";
import { AuditTable, useAuditPager } from "@/components/audit-table";
import { DataTable, type Column } from "@/components/data-table";
import { AmountCell } from "@/components/money";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { useAssetLookup, useCentralBankAudit, useReserves } from "@/lib/api/hooks";
import type { Reserve } from "@/lib/types";

// A reserve's amount can only be rendered once its asset's scale is resolved
// from the *reserve-holding participant's* own book-scoped asset registry
// (see ledger.Book.CreateAsset) — reserves span every participant, so this
// resolves per row rather than once for the page.
function ReserveAmountCell({ reserve }: { reserve: Reserve }) {
  const { byCode, isLoading } = useAssetLookup(reserve.participant);
  const asset = byCode.get(reserve.asset);
  if (!asset) return isLoading ? <Skeleton className="ml-auto h-4 w-16" /> : null;
  return <AmountCell amount={reserve.reserve} asset={asset} />;
}

const reserveColumns: Column<Reserve>[] = [
  {
    key: "participant",
    header: "Participant",
    render: (r) => <IdText id={r.participant} />,
  },
  {
    key: "asset",
    header: "Asset",
    render: (r) => r.asset,
  },
  {
    key: "reserve",
    header: "Reserve",
    align: "right",
    hint: "reserve-account",
    render: (r) => <ReserveAmountCell reserve={r} />,
  },
];

export default function CentralBankPage() {
  const reserves = useReserves();
  const pager = useAuditPager();
  const audit = useCentralBankAudit(pager.query);

  return (
    <div className="space-y-8">
      <PageHeader
        title="Central bank"
        hint="central-bank-reserves"
        description="Banks meet only here. The central bank holds one reserve account per participant and asset, and settlement is reserves moving between them."
      />

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Reserves</h2>
        {reserves.error ? (
          <ErrorState error={reserves.error} onRetry={() => reserves.refetch()} />
        ) : (
          <DataTable
            columns={reserveColumns}
            rows={reserves.data}
            rowKey={(r) => `${r.participant}:${r.asset}`}
            isLoading={reserves.isLoading}
            empty="No participants yet. Create one to see its reserve account."
          />
        )}
      </section>

      <section className="space-y-3">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Audit trail
        </h2>
        <AuditTable
          events={audit.data}
          isLoading={audit.isLoading}
          error={audit.error}
          onRetry={() => audit.refetch()}
          pager={pager}
          empty="No central-bank activity yet. Fund a participant or settle a cycle to see reserve movements."
        />
      </section>
    </div>
  );
}
