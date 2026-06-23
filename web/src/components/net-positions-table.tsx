"use client";

import { DataTable, type Column } from "@/components/data-table";
import { AmountCell } from "@/components/money";
import { CopyId } from "@/components/copy-id";

interface NetPosition {
  participant: string;
  amount: number;
}

// Renders a clearing cycle's / settlement's net positions: one signed number
// per participant. Positive = net receiver (owed money); negative = net payer
// (owes money). The whole table sums to zero — money is conserved.
export function NetPositionsTable({
  positions,
}: {
  positions?: Record<string, number>;
}) {
  const rows: NetPosition[] = positions
    ? Object.entries(positions)
        .map(([participant, amount]) => ({ participant, amount }))
        .sort((a, b) => b.amount - a.amount)
    : [];

  const columns: Column<NetPosition>[] = [
    {
      key: "participant",
      header: "Participant",
      hint: "net-positions",
      render: (r) => <CopyId id={r.participant} />,
    },
    {
      key: "amount",
      header: "Net position",
      hint: "netting",
      align: "right",
      render: (r) => <AmountCell cents={r.amount} signed />,
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
