"use client";

import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { CopyId } from "@/components/copy-id";
import { Money } from "@/components/money";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/error-state";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { CreateMandateForm } from "@/components/forms/create-mandate-form";
import { useMandates, useRevokeMandate } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { Mandate } from "@/lib/types";

function RevokeButton({ mandate }: { mandate: Mandate }) {
  const revoke = useRevokeMandate();
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
  const { data, isLoading, error, refetch } = useMandates();

  const columns: Column<Mandate>[] = [
    { key: "id", header: "ID", render: (m) => <CopyId id={m.id} /> },
    {
      key: "debtor",
      header: "Debtor",
      render: (m) => <CopyId id={m.debtor.participant} />,
    },
    {
      key: "creditor",
      header: "Creditor",
      render: (m) => <CopyId id={m.creditor.participant} />,
    },
    {
      key: "maxAmount",
      header: "Max amount",
      align: "right",
      render: (m) => <Money cents={m.maxAmount} />,
    },
    { key: "status", header: "Status", render: (m) => <EnumBadge value={m.status} /> },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (m) => <RevokeButton mandate={m} />,
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Mandates"
        hint="mandate"
        description="Standing authorizations that let a creditor pull funds from a debtor — required by pull (direct-debit) schemes."
        actions={<CreateMandateForm />}
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(m) => m.id}
          isLoading={isLoading}
          empty="No mandates yet. Create one to enable pull payments."
        />
      )}
    </div>
  );
}
