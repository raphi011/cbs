"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import { toast } from "sonner";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge, DirectionBadge } from "@/components/enum-badge";
import { CopyId } from "@/components/copy-id";
import { Money } from "@/components/money";
import { Hint } from "@/components/hint";
import { ErrorState } from "@/components/error-state";
import { PostTransactionForm } from "@/components/forms/post-transaction-form";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { useReverseTransaction, useTransactions } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { formatDate } from "@/lib/dates";
import type { Transaction } from "@/lib/types";

export default function TransactionsPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useTransactions(pid);
  const reverse = useReverseTransaction(pid);
  const [selected, setSelected] = useState<Transaction | null>(null);

  const columns: Column<Transaction>[] = [
    { key: "id", header: "ID", render: (t) => <CopyId id={t.id} /> },
    {
      key: "status",
      header: "Status",
      render: (t) => <EnumBadge value={t.status} />,
    },
    {
      key: "legs",
      header: "Legs",
      render: (t) => `${t.entries.length}`,
    },
    {
      key: "description",
      header: "Description",
      render: (t) => t.description || "—",
    },
    {
      key: "valueDate",
      header: "Value date",
      hint: "value-date",
      render: (t) => formatDate(t.valueDate),
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium text-muted-foreground">
          Transactions
        </h2>
        <PostTransactionForm pid={pid} />
      </div>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={data}
          rowKey={(t) => t.id}
          isLoading={isLoading}
          onRowClick={(t) => setSelected(t)}
          empty="No transactions yet. Post one to see double-entry in action."
        />
      )}

      <Dialog open={!!selected} onOpenChange={(o) => !o && setSelected(null)}>
        <DialogContent className="sm:max-w-lg">
          {selected && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2">
                  Transaction <CopyId id={selected.id} />
                </DialogTitle>
                <DialogDescription>
                  {selected.description || "No description"}
                </DialogDescription>
              </DialogHeader>

              <div className="space-y-3">
                <div className="flex items-center gap-2 text-sm">
                  <EnumBadge value={selected.status} />
                  {selected.reversalOf && (
                    <span className="text-muted-foreground">
                      reversal of <CopyId id={selected.reversalOf} />
                    </span>
                  )}
                </div>

                <div className="rounded-md border divide-y">
                  {selected.entries.map((e, i) => (
                    <div
                      key={e.id ?? i}
                      className="flex items-center justify-between gap-2 px-3 py-2"
                    >
                      <span className="flex items-center gap-2">
                        <DirectionBadge direction={e.direction} />
                        <CopyId id={e.accountId} />
                      </span>
                      <Money cents={e.amount} />
                    </div>
                  ))}
                </div>

                {selected.status === "Posted" && (
                  <div className="flex items-center justify-between gap-2 pt-1">
                    <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
                      Made an error?
                      <Hint id="reversal" />
                    </span>
                    <ConfirmAction
                      trigger={
                        <Button variant="destructive" size="sm">
                          Reverse
                        </Button>
                      }
                      title="Reverse transaction"
                      description="Posts a new transaction that flips every debit and credit, netting to zero. The original stays on record, marked Reversed."
                      confirmLabel="Reverse"
                      destructive
                      pending={reverse.isPending}
                      input={{
                        label: "Reason / description",
                        placeholder: "e.g. duplicate posting",
                      }}
                      onConfirm={async (description) => {
                        await reverse.mutateAsync(
                          { tid: selected.id, description },
                          {
                            onSuccess: () => {
                              toast.success("Transaction reversed");
                              setSelected(null);
                            },
                            onError: (err) => toast.error(describeError(err)),
                          },
                        );
                      }}
                    />
                  </div>
                )}
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
