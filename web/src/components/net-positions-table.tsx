"use client";

import { DataTable, type Column } from "@/components/data-table";
import { AmountCell } from "@/components/money";
import { IdText } from "@/components/id-text";
import { Skeleton } from "@/components/ui/skeleton";
import { useAssetLookup } from "@/lib/api/hooks";

interface NetPosition {
  participant: string;
  amount: number;
}

// Renders a clearing cycle's / settlement's net positions: one signed number
// per participant. Positive = net receiver (owed money); negative = net payer
// (owes money). The whole table sums to zero — money is conserved.
//
// `asset` is the cycle's/settlement's asset code (both DTOs now carry one,
// resolved server-side from the scheme — see api/dto_payment.go's
// toClearingCycleDTO/toSettlementDTO). Its scale still has to be resolved
// against a participant's own book-scoped registry (see
// ledger.Book.CreateAsset); any participant listed in `positions` will do,
// since a code names the same asset network-wide by construction.
export function NetPositionsTable({
  positions,
  asset,
}: {
  positions?: Record<string, number>;
  asset: string;
}) {
  const rows: NetPosition[] = positions
    ? Object.entries(positions)
        .map(([participant, amount]) => ({ participant, amount }))
        .sort((a, b) => b.amount - a.amount)
    : [];

  const { byCode, isLoading } = useAssetLookup(rows[0]?.participant ?? "");
  const resolvedAsset = byCode.get(asset);

  const columns: Column<NetPosition>[] = [
    {
      key: "participant",
      header: "Participant",
      hint: "net-positions",
      render: (r) => <IdText id={r.participant} />,
    },
    {
      key: "amount",
      header: "Net position",
      hint: "netting",
      align: "right",
      render: (r) =>
        resolvedAsset ? (
          <AmountCell amount={r.amount} asset={resolvedAsset} signed />
        ) : isLoading ? (
          <Skeleton className="ml-auto h-4 w-16" />
        ) : (
          <span className="block text-right text-muted-foreground">—</span>
        ),
    },
  ];

  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(r) => r.participant}
      empty="No net positions yet — close the cycle to compute them."
    />
  );
}
