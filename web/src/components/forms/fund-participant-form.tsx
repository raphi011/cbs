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

// Funds a deposit account: credits the customer's deposit and raises the bank's
// central-bank reserve in step. This is how reserves (which start at 0) are
// seeded — the entry point of the whole money loop.
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
      toast.success("Funded — reserve raised in step");
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
              Credits this account and raises the bank&apos;s central-bank
              reserve by the same amount.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="fund-amount" hint="central-bank-reserves" required>
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
