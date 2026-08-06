"use client";

import { useState } from "react";
import { Landmark } from "lucide-react";
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
import { useLodgeReserves } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { Asset } from "@/lib/types";

// Places a bank's vault cash on reserve at the central bank: the lodgement, and
// the second half of what funding used to be.
//
// # Why this is a form at all
//
// Until Task 18a there was nothing here to do. A deposit credited the customer
// and raised the bank's central-bank reserve in the same unit of work, so
// reserves appeared as a side effect of cash arriving and no operator ever chose
// to move them. That worked only because one store held both books.
//
// A bank cannot write in the central bank's ledger. So cash in stops at the
// bank's own vault (see FundParticipantForm) and moving it onward is a REQUEST
// the bank sends its central bank — a camt.050, answered by a camt.025. This form
// is where a bank's operator makes that decision, which is a real decision a real
// treasury function makes every day.
//
// # It is a per-ASSET action, not a per-account one
//
// A lodgement moves the bank's own cash, of which there is one pot per asset, so
// the asset is what identifies the pot. FundParticipantForm sits on a deposit
// account and takes the asset from it; this one is handed the asset because the
// caller is a row of the reserves card, which is already one row per asset.
//
// # The reserve is not up when this resolves
//
// The route answers 202: the credit is the central bank's to make, on a message
// still in flight. The toast says "asked" rather than "done" for that reason, and
// the hook invalidates anyway so the figure refreshes when the answer lands. In
// this system the mesh delivers synchronously inside one process, so the refetch
// will see it — but that is the transport's doing and not this form's promise.
export function LodgeReservesForm({
  pid,
  asset,
}: {
  pid: string;
  asset: Asset;
}) {
  const [open, setOpen] = useState(false);
  const [amount, setAmount] = useState<number | null>(null);
  const lodge = useLodgeReserves(pid);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (amount == null) return;
    try {
      await lodge.mutateAsync({ asset: asset.code, amount });
      toast.success(`Lodgement sent — the central bank was asked to credit ${asset.code}`);
      setAmount(null);
      setOpen(false);
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Landmark className="size-4" />
          Lodge
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Lodge {asset.code} on reserve</DialogTitle>
            <DialogDescription>
              Asks the central bank to credit this bank&apos;s reserve account,
              against vault cash the bank hands over. Two books move, one at each
              institution, and the reserve is credited when the central bank
              answers.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="lodge-amount" hint="lodgement" required>
              Amount
            </FieldLabel>
            <MoneyInput
              id="lodge-amount"
              value={amount}
              onChange={setAmount}
              asset={asset}
            />
          </div>
          <DialogFooter>
            <Button type="submit" disabled={lodge.isPending || amount == null}>
              {lodge.isPending ? "Lodging…" : "Lodge"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
