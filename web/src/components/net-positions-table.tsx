"use client";

import { DataTable, type Column } from "@/components/data-table";
import { AmountCell, UnresolvedAmount } from "@/components/money";
import { IdText } from "@/components/id-text";
import { useAssetLookup } from "@/lib/api/hooks";

interface NetPosition {
  agent: string;
  amount: number;
}

// Renders a clearing cycle's / settlement's net positions: one signed number
// per bank, keyed by the BIC the settlement instruction addresses it at.
// Positive = net receiver (owed money); negative = net payer (owes money). The
// whole table sums to zero — money is conserved.
//
// `asset` is the cycle's/settlement's asset code (both DTOs carry one,
// resolved server-side from the scheme — see api/dto_payment.go's
// toClearingCycleDTO/toSettlementDTO). Its scale comes from the network-wide
// asset list.
export function NetPositionsTable({
  positions,
  asset,
}: {
  positions?: Record<string, number>;
  asset: string;
}) {
  const rows: NetPosition[] = positions
    ? Object.entries(positions)
        .map(([agent, amount]) => ({ agent, amount }))
        .sort((a, b) => b.amount - a.amount)
    : [];

  const { byCode, isLoading } = useAssetLookup();
  const resolvedAsset = byCode.get(asset);

  const columns: Column<NetPosition>[] = [
    {
      key: "agent",
      header: "Bank (BIC)",
      hint: "net-positions",
      render: (r) => <IdText id={r.agent} />,
    },
    {
      key: "amount",
      header: "Net position",
      hint: "netting",
      align: "right",
      render: (r) =>
        resolvedAsset ? (
          <AmountCell amount={r.amount} asset={resolvedAsset} signed />
        ) : (
          <UnresolvedAmount code={asset} isLoading={isLoading} className="ml-auto block text-right" />
        ),
    },
  ];

  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(r) => r.agent}
      empty="No net positions yet — close the cycle to compute them."
    />
  );
}
