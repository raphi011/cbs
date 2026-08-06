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
// nothing to resolve a scale from the way a payment's asset is resolved from its
// scheme. What fixes it is the CREDITOR's account — the mandate is recorded at
// the creditor's bank and MaxAmount is denominated in the account that bank
// holds (see payment.Mandate.Asset) — so this form resolves the same way the
// backend does, and it can, because that account is this bank's own.
//
// It used to resolve from the DEBTOR's account and it could not honestly: the
// debtor banks somewhere else, so the form was reading another bank's deposit
// register over HTTP for a scale, exactly as the backend was.
export function CreateMandateForm({ pid }: { pid: string }) {
  const [open, setOpen] = useState(false);
  const [debtor, setDebtor] = useState<PartyRef>(emptyPartyRef);
  const [creditorAccount, setCreditorAccount] = useState("");
  const [maxAmount, setMaxAmount] = useState<number | null>(null);
  const create = useCreateMandate(pid);
  const ownAccounts = useDepositAccounts(pid);
  const creditorAcct = ownAccounts.data?.find((a) => a.id === creditorAccount);
  const { byCode } = useAssetLookup();
  const resolvedAsset = creditorAcct ? byCode.get(creditorAcct.asset) : undefined;
  const creditor: PartyRef = { participant: pid, account: creditorAccount };

  const valid =
    debtor.participant.trim() &&
    debtor.account.trim() &&
    creditorAccount.trim() &&
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
      setCreditorAccount("");
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
              Authorizes one of this bank&apos;s customers to pull funds from an
              account at another bank, up to the maximum amount.
            </DialogDescription>
          </DialogHeader>
          {/* The debtor is somebody else's customer, so it is TYPED rather
              than chosen: this bank has no register to pick it out of, which is
              the whole reason CreateMandateTx records it instead of checking
              it. */}
          <PartyRefFields legend="Debtor" value={debtor} onChange={setDebtor} />
          {/* The creditor is one of this bank's own accounts, so it is a
              choice. It is also what fixes the mandate's asset. */}
          <div className="space-y-2">
            <FieldLabel htmlFor="mandate-creditor" required>
              Creditor account (this bank&apos;s)
            </FieldLabel>
            <select
              id="mandate-creditor"
              className="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs"
              value={creditorAccount}
              onChange={(e) => setCreditorAccount(e.target.value)}
            >
              <option value="">Select an account…</option>
              {(ownAccounts.data ?? []).map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} ({a.asset})
                </option>
              ))}
            </select>
          </div>
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
                Choose the creditor&apos;s deposit account to resolve the
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
