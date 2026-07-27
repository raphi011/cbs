"use client";

import { useState } from "react";
import { Plus } from "lucide-react";
import { toast } from "sonner";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { FieldLabel } from "@/components/field-label";
import { MoneyInput } from "@/components/money";
import { PartyRefFields, emptyPartyRef } from "@/components/forms/party-ref-fields";
import { useAssetLookup, useCreateMandate, useDepositAccounts } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { PartyRef } from "@/lib/types";

// A mandate names no scheme (createMandateRequest has neither), so there is
// nothing to resolve a scale from the way a payment's asset is resolved from
// its scheme. What fixes the mandate's asset is the debtor account being
// authorized to pull from — MaxAmount is denominated in that account's asset
// (see api/dto_payment.go's toMandateDTO) — so this form resolves the same
// way the backend does: the debtor's own deposit account.
export function CreateMandateForm() {
  const [open, setOpen] = useState(false);
  const [debtor, setDebtor] = useState<PartyRef>(emptyPartyRef);
  const [creditor, setCreditor] = useState<PartyRef>(emptyPartyRef);
  const [maxAmount, setMaxAmount] = useState<number | null>(null);
  const create = useCreateMandate();
  const debtorAccounts = useDepositAccounts(debtor.participant);
  const debtorAccount = debtorAccounts.data?.find((a) => a.id === debtor.account);
  const { byCode } = useAssetLookup(debtor.participant);
  const resolvedAsset = debtorAccount ? byCode.get(debtorAccount.asset) : undefined;

  const valid =
    debtor.participant.trim() &&
    debtor.account.trim() &&
    creditor.participant.trim() &&
    creditor.account.trim() &&
    resolvedAsset != null &&
    maxAmount != null;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!valid) return;
    try {
      const m = await create.mutateAsync({
        debtor,
        creditor,
        maxAmount: maxAmount!,
      });
      toast.success(`Mandate created (${m.id})`);
      setDebtor(emptyPartyRef);
      setCreditor(emptyPartyRef);
      setMaxAmount(null);
      setOpen(false);
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus className="size-4" />
          New mandate
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Create mandate</DialogTitle>
            <DialogDescription>
              Authorizes the creditor to pull funds from the debtor, up to the
              maximum amount.
            </DialogDescription>
          </DialogHeader>
          <PartyRefFields legend="Debtor" value={debtor} onChange={setDebtor} />
          <PartyRefFields
            legend="Creditor"
            value={creditor}
            onChange={setCreditor}
          />
          <div className="space-y-2">
            <FieldLabel htmlFor="mandate-max" required>
              Maximum amount
            </FieldLabel>
            {resolvedAsset ? (
              <MoneyInput
                id="mandate-max"
                value={maxAmount}
                onChange={setMaxAmount}
                asset={resolvedAsset}
              />
            ) : (
              <p className="text-xs text-muted-foreground">
                Choose the debtor&apos;s deposit account to resolve the
                mandate&apos;s asset.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={create.isPending || !valid}>
              {create.isPending ? "Creating…" : "Create mandate"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
