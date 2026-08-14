"use client";

import { useParams } from "next/navigation";
import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { IdText } from "@/components/id-text";
import { Money, UnresolvedAmount } from "@/components/money";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/error-state";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { CreateMandateForm } from "@/components/forms/create-mandate-form";
import { useAssetLookup, useMandates, useRevokeMandate } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { Mandate } from "@/lib/types";

// This screen is a BANK's, not the clearing house's.
//
// A mandate is the CREDITOR's bank's row: in SEPA the creditor holds the
// mandate, and the bank that checks one at submission is the creditor's. On the
// clearing house it would be every member's authorisations over every other
// member's customers' accounts on one page — and rendering each amount would
// mean loading the DEBTOR's bank and listing its deposit register for the asset,
// one institution reading another's for a display field.
//
// So this lists the mandates whose creditor is THIS bank's own customer, and
// nothing else. There is no debtor-side screen: a debtor's bank holds no mandate
// row in this system and has no message that would give it one, which is the
// limit payment.SDD.ValidateMandate already names.

// A mandate's asset now comes off the ROW — mandateDTO reads it from
// payment.Mandate.Asset, filled at creation from the creditor's own account —
// and the scale it implies comes from the network-wide asset list.
function MandateAmountCell({ mandate }: { mandate: Mandate }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(mandate.asset);
  if (!asset) {
    return (
      <UnresolvedAmount code={mandate.asset} isLoading={isLoading} className="ml-auto block text-right" />
    );
  }
  return <Money amount={mandate.maxAmount} asset={asset} />;
}

function RevokeButton({ pid, mandate }: { pid: string; mandate: Mandate }) {
  const revoke = useRevokeMandate(pid);
  if (mandate.status !== "Active") {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <ConfirmAction
      trigger={
        <Button variant="ghost" size="sm">
          Revoke
        </Button>
      }
      title="Revoke mandate"
      description="Cancels the standing authorization. Future pulls under it will be rejected."
      confirmLabel="Revoke"
      destructive
      pending={revoke.isPending}
      onConfirm={async () => {
        await revoke.mutateAsync(mandate.id, {
          onSuccess: () => toast.success("Mandate revoked"),
          onError: (err) => toast.error(describeError(err)),
        });
      }}
    />
  );
}

export default function MandatesPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useMandates(pid);

  const columns: Column<Mandate>[] = [
    { key: "id", header: "ID", render: (m) => <IdText id={m.id} /> },
    {
      // The debtor's bank and, under it, the address the collection quotes. The
      // mandate records both and resolves neither: the account is in the debtor
      // bank's register, which is why the collection is addressed to it.
      key: "debtor",
      header: "Debtor",
      render: (m) => (
        <span className="flex flex-col gap-0.5">
          <IdText id={m.debtorAgent ?? "—"} />
          <IdText id={m.debtor.identifier?.value || m.debtor.account || "—"} />
        </span>
      ),
    },
    {
      key: "creditor",
      header: "Creditor",
      render: (m) => <IdText id={m.creditor.account} />,
    },
    {
      key: "maxAmount",
      header: "Max amount",
      align: "right",
      render: (m) => <MandateAmountCell mandate={m} />,
    },
    { key: "status", header: "Status", render: (m) => <EnumBadge value={m.status} /> },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (m) => <RevokeButton pid={pid} mandate={m} />,
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Mandates"
        hint="mandate"
        description="Standing authorizations this bank's own customers hold, letting them pull funds from an account at another bank. Required by pull (direct-debit) schemes, and held by the creditor's bank — which is this one."
        actions={<CreateMandateForm pid={pid} />}
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(m) => m.id}
          isLoading={isLoading}
          empty="No mandates yet. Create one to let a customer of this bank collect by direct debit, or run a scenario from the topbar."
        />
      )}
    </div>
  );
}
