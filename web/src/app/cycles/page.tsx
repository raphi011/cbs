"use client";

import { useRouter } from "next/navigation";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { CopyId } from "@/components/copy-id";
import { ErrorState } from "@/components/error-state";
import { OpenCycleForm } from "@/components/forms/open-cycle-form";
import { useCycles } from "@/lib/api/hooks";
import { formatDateTime } from "@/lib/dates";
import type { ClearingCycle } from "@/lib/types";

export default function CyclesPage() {
  const router = useRouter();
  const { data, isLoading, error, refetch } = useCycles();

  const columns: Column<ClearingCycle>[] = [
    { key: "id", header: "ID", render: (c) => <CopyId id={c.id} /> },
    { key: "scheme", header: "Scheme", render: (c) => c.scheme },
    { key: "status", header: "Status", render: (c) => <EnumBadge value={c.status} /> },
    {
      key: "payments",
      header: "Payments",
      align: "right",
      render: (c) => `${c.paymentIds.length}`,
    },
    {
      key: "openedAt",
      header: "Opened",
      render: (c) => formatDateTime(c.openedAt),
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Clearing cycles"
        hint="clearing-vs-settlement"
        description="A cycle batches a scheme's payments. Closing it nets each participant's position; settling moves the net amounts across central-bank reserves."
        actions={<OpenCycleForm />}
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(c) => c.id}
          isLoading={isLoading}
          onRowClick={(c) => router.push(`/cycles/${c.id}`)}
          empty="No cycles yet. Open one for a scheme, then route payments into it."
        />
      )}
    </div>
  );
}
