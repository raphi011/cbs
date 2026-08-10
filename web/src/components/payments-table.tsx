"use client";

import { ArrowRight } from "lucide-react";

import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { IdText } from "@/components/id-text";
import { Money, UnresolvedAmount } from "@/components/money";
import { useAssetLookup } from "@/lib/api/hooks";
import type { Payment } from "@/lib/types";

// A payment carries its own asset code, fixed by its scheme. The scale that code
// implies lives only in the network-wide asset list, which every caller shares
// through useAssetLookup (one GET /assets). Until the code resolves there is no
// scale to render at, and guessing one is the bug the whole asset dimension
// exists to prevent — so withhold the number instead.
function PaymentAmountCell({ payment }: { payment: Payment }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(payment.asset);
  if (!asset) {
    return (
      <UnresolvedAmount
        code={payment.asset}
        isLoading={isLoading}
        className="ml-auto block text-right"
      />
    );
  }
  return <Money amount={payment.amount} asset={asset} />;
}

const columns: Column<Payment>[] = [
  { key: "id", header: "ID", render: (p) => <IdText id={p.id} /> },
  { key: "scheme", header: "Scheme", render: (p) => p.scheme },
  {
    // The two BANKS, by the addresses the messages carried. A payment names no
    // participant on either side, and the accounts it does name are each held in
    // a different bank's register — so the pair that means the same thing on
    // every row is the agents.
    key: "flow",
    header: "Debtor → Creditor",
    render: (p) => (
      <span className="flex items-center gap-1.5">
        <IdText id={p.debtorAgent ?? "—"} />
        <ArrowRight className="size-3.5 text-muted-foreground" />
        <IdText id={p.creditorAgent ?? "—"} />
      </span>
    ),
  },
  { key: "amount", header: "Amount", align: "right", render: (p) => <PaymentAmountCell payment={p} /> },
  { key: "status", header: "Status", render: (p) => <EnumBadge value={p.status} /> },
];

// Shared by the clearing house's list of every payment and a bank's list of its
// own legs. The same rows either way; what differs is which listener answered,
// and therefore how many there are.
//
// onRowClick is optional because only the clearing house has a payment detail
// page. A bank's rows go nowhere: the spec gives it a list and no drill-down, and
// a row that looks clickable and is not would be worse than one that does not.
export function PaymentsTable({
  rows,
  isLoading,
  onRowClick,
  empty,
}: {
  rows: Payment[] | undefined;
  isLoading: boolean;
  onRowClick?: (p: Payment) => void;
  empty: string;
}) {
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(p) => p.id}
      isLoading={isLoading}
      onRowClick={onRowClick}
      empty={empty}
    />
  );
}
