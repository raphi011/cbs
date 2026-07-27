"use client";

import { useRouter } from "next/navigation";
import { ArrowRight } from "lucide-react";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { IdText } from "@/components/id-text";
import { Money, UnresolvedAmount } from "@/components/money";
import { ErrorState } from "@/components/error-state";
import { InitiatePaymentForm } from "@/components/forms/initiate-payment-form";
import { useAssetLookup, usePayments } from "@/lib/api/hooks";
import type { Payment } from "@/lib/types";

// A payment carries its own asset code, fixed by its scheme. The scale that
// code implies lives only in the network-wide asset list, which every caller
// shares through useAssetLookup (one GET /assets). Until the code resolves
// there is no scale to render at, and guessing one is the bug the whole asset
// dimension exists to prevent — so withhold the number instead.
function PaymentAmountCell({ payment }: { payment: Payment }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(payment.asset);
  if (!asset) {
    return (
      <UnresolvedAmount code={payment.asset} isLoading={isLoading} className="ml-auto block text-right" />
    );
  }
  return <Money amount={payment.amount} asset={asset} />;
}

export default function PaymentsPage() {
  const router = useRouter();
  const { data, isLoading, error, refetch } = usePayments();

  const columns: Column<Payment>[] = [
    { key: "id", header: "ID", render: (p) => <IdText id={p.id} /> },
    { key: "scheme", header: "Scheme", render: (p) => p.scheme },
    {
      key: "flow",
      header: "Debtor → Creditor",
      render: (p) => (
        <span className="flex items-center gap-1.5">
          <IdText id={p.debtor.participant} />
          <ArrowRight className="size-3.5 text-muted-foreground" />
          <IdText id={p.creditor.participant} />
        </span>
      ),
    },
    {
      key: "amount",
      header: "Amount",
      align: "right",
      render: (p) => <PaymentAmountCell payment={p} />,
    },
    { key: "status", header: "Status", render: (p) => <EnumBadge value={p.status} /> },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Payments"
        hint="payment-lifecycle"
        description="Every payment moves money debtor → creditor under a scheme, then progresses through its lifecycle: initiated → accepted → cleared → settled."
        actions={<InitiatePaymentForm />}
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(p) => p.id}
          isLoading={isLoading}
          onRowClick={(p) => router.push(`/payments/${p.id}`)}
          empty="No payments yet. Initiate one between two funded participants."
        />
      )}
    </div>
  );
}
