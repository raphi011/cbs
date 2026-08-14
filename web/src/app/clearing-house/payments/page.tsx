"use client";

import { useRouter } from "next/navigation";

import { PageHeader } from "@/components/page-header";
import { ErrorState } from "@/components/error-state";
import { InitiatePaymentForm } from "@/components/forms/initiate-payment-form";
import { PaymentsTable } from "@/components/payments-table";
import { usePayments } from "@/lib/api/hooks";

export default function PaymentsPage() {
  const router = useRouter();
  const { data, isLoading, error, refetch } = usePayments();

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
        <PaymentsTable
          rows={data}
          isLoading={isLoading}
          onRowClick={(p) => router.push(`/clearing-house/payments/${p.id}`)}
          empty="No payments yet. A fresh deployment holds four banks and no customers — run a scenario from the topbar to make one, or initiate a payment between two funded accounts yourself."
        />
      )}
    </div>
  );
}
