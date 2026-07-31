"use client";

import { useRouter } from "next/navigation";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { useSettlements } from "@/lib/api/hooks";
import { formatDateTime } from "@/lib/dates";
import type { Settlement } from "@/lib/types";

export default function SettlementsPage() {
  const router = useRouter();
  const { data, isLoading, error, refetch } = useSettlements();

  const columns: Column<Settlement>[] = [
    { key: "id", header: "ID", render: (s) => <IdText id={s.id} /> },
    { key: "cycleId", header: "Cycle", render: (s) => <IdText id={s.cycleId} /> },
    {
      key: "participants",
      header: "Participants",
      align: "right",
      render: (s) => `${Object.keys(s.netPositions).length}`,
    },
    {
      key: "settledAt",
      header: "Settled",
      render: (s) => formatDateTime(s.settledAt),
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Settlements"
        hint="clearing-vs-settlement"
        description="The final, irrevocable movement of money across central-bank reserves that discharges a closed cycle's net positions."
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(s) => s.id}
          isLoading={isLoading}
          onRowClick={(s) => router.push(`/clearing-house/settlements/${s.id}`)}
          empty="No settlements yet. Close and settle a clearing cycle to create one."
        />
      )}
    </div>
  );
}
