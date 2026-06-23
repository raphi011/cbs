"use client";

import { useRouter } from "next/navigation";
import { ArrowRight } from "lucide-react";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { CopyId } from "@/components/copy-id";
import { Money } from "@/components/money";
import { ErrorState } from "@/components/error-state";
import { InitiatePaymentForm } from "@/components/forms/initiate-payment-form";
import { usePayments } from "@/lib/api/hooks";
import type { Payment } from "@/lib/types";

export default function PaymentsPage() {
  const router = useRouter();
  const { data, isLoading, error, refetch } = usePayments();

  const columns: Column<Payment>[] = [
    { key: "id", header: "ID", render: (p) => <CopyId id={p.id} /> },
    { key: "scheme", header: "Scheme", render: (p) => p.scheme },
    {
      key: "flow",
      header: "Debtor → Creditor",
      render: (p) => (
        <span className="flex items-center gap-1.5">
          <CopyId id={p.debtor.participant} />
          <ArrowRight className="size-3.5 text-muted-foreground" />
          <CopyId id={p.creditor.participant} />
        </span>
      ),
    },
    {
      key: "amount",
      header: "Amount",
      align: "right",
      render: (p) => <Money cents={p.amount} />,
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
