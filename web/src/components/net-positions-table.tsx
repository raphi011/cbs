"use client";

import { DataTable, type Column } from "@/components/data-table";
import { AmountCell } from "@/components/money";
import { IdText } from "@/components/id-text";

interface NetPosition {
  participant: string;
  amount: number;
}

// clearingCycleDTO/settlementDTO carry net positions with no asset field —
// a cycle only names a scheme, and schemeDTO itself has no asset field (the
// API resolves a payment's asset from its scheme server-side, but doesn't
// expose that resolution for a scheme or cycle directly). Every scheme
// implemented so far settles in EUR (see payment/scheme.go's SCT/SDD), so
// this is that fact made explicit and grep-able, not a guessed default — it
// stops being true, and needs revisiting, the day a non-EUR scheme ships.
const NET_POSITION_ASSET = { code: "EUR", scale: 2 };

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
      render: (r) => <IdText id={r.participant} />,
    },
    {
      key: "amount",
      header: "Net position",
      hint: "netting",
      align: "right",
      render: (r) => <AmountCell amount={r.amount} asset={NET_POSITION_ASSET} signed />,
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
