"use client";

import { useParams } from "next/navigation";

import { PageHeader } from "@/components/page-header";
import { ErrorState } from "@/components/error-state";
import { PaymentsTable } from "@/components/payments-table";
import { useBankPayments } from "@/lib/api/hooks";

// A bank's own legs — the payments it sent and the ones it received, and nothing
// else. This screen could not have existed before the operator split: the single
// server's GET /payments listed every payment in the network to every caller,
// because narrowing needs a caller identity and there was none. The port is that
// identity now.
//
// There is no drill-down. A payment's detail page is the clearing house's, and
// following one would walk the back office out of its own console.
export default function BankPaymentsPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useBankPayments(pid);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Payments"
        hint="payment-lifecycle"
        description="The payments this bank is a party to, as debtor or as creditor. Another bank's customers, counterparties and amounts are not here — and are not reachable from here by editing a URL, because this listener has no route that names another bank."
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <PaymentsTable
          rows={data}
          isLoading={isLoading}
          empty="No payments yet. This bank has neither sent nor received one — run a scenario from the topbar to give it some traffic."
        />
      )}
    </div>
  );
}
