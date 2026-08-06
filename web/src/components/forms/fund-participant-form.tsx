"use client";

import { useState } from "react";
import { Banknote } from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { FieldLabel } from "@/components/field-label";
import { MoneyInput } from "@/components/money";
import { useFundDeposit } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { Asset } from "@/lib/types";

// Takes cash in over the counter: credits the customer's deposit, and leaves the
// bank holding the cash as vault cash.
//
// It does NOT raise the bank's central-bank reserve, and this comment used to say
// it did — "this is how reserves (which start at 0) are seeded". Since Task 18a
// that is the LODGEMENT's job (see LodgeReservesForm): a bank cannot write in the
// central bank's ledger, so moving cash onto reserve is a request it sends rather
// than an entry it makes.
//
// So this is still the entry point of the money loop and no longer the whole of
// its first step. Cash in, then lodge, then the bank can settle.
export function FundParticipantForm({
  pid,
  did,
  asset,
}: {
  pid: string;
  did: string;
  asset: Asset;
}) {
  const [open, setOpen] = useState(false);
  const [amount, setAmount] = useState<number | null>(null);
  const [description, setDescription] = useState("");
  const fund = useFundDeposit(pid);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (amount == null) return;
    try {
      await fund.mutateAsync({
        account: did,
        amount,
        description: description.trim() || undefined,
      });
      toast.success("Cash in — held as vault cash");
      setAmount(null);
      setDescription("");
      setOpen(false);
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Banknote className="size-4" />
          Fund
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Fund deposit account</DialogTitle>
            <DialogDescription>
              Credits this account. The bank holds the cash as vault cash —
              putting it on reserve at the central bank is a separate lodgement.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="fund-amount" hint="vault-cash" required>
              Amount
            </FieldLabel>
            <MoneyInput
              id="fund-amount"
              value={amount}
              onChange={setAmount}
              asset={asset}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="fund-desc">Description (optional)</FieldLabel>
            <Input
              id="fund-desc"
              value={description}
              placeholder="e.g. initial deposit"
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={fund.isPending || amount == null}
            >
              {fund.isPending ? "Funding…" : "Fund"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
