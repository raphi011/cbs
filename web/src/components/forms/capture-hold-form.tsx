"use client";

import { useState } from "react";
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
import { GLAccountPicker } from "@/components/pickers/gl-account-picker";
import { useCaptureHold } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Captures a hold: posts a real ledger transaction debiting the customer and
// crediting a counterparty GL account, for the final amount (up to the held
// amount). The counterparty is a GL account id — copy it from the General
// ledger tab.
export function CaptureHoldForm({
  pid,
  hid,
  heldAmount,
}: {
  pid: string;
  hid: string;
  heldAmount: number;
}) {
  const [open, setOpen] = useState(false);
  const [counterparty, setCounterparty] = useState("");
  const [amountCents, setAmountCents] = useState<number | null>(heldAmount);
  const [description, setDescription] = useState("");
  const capture = useCaptureHold(pid);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!counterparty.trim() || amountCents == null) return;
    try {
      const tx = await capture.mutateAsync({
        hid,
        body: {
          counterparty: counterparty.trim(),
          amount: amountCents,
          description: description.trim() || undefined,
        },
      });
      toast.success(`Captured — posted ${tx.id}`);
      setCounterparty("");
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
          Capture
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Capture hold</DialogTitle>
            <DialogDescription>
              Turns the reservation into a posted transaction debiting the
              customer and crediting the counterparty.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="cap-counterparty" hint="hold-capture" required>
              Counterparty GL account
            </FieldLabel>
            <GLAccountPicker
              id="cap-counterparty"
              pid={pid}
              value={counterparty}
              onChange={setCounterparty}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="cap-amount" required>
              Amount
            </FieldLabel>
            <MoneyInput
              id="cap-amount"
              valueCents={amountCents}
              onChangeCents={setAmountCents}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="cap-desc">Description (optional)</FieldLabel>
            <Input
              id="cap-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={
                capture.isPending || !counterparty.trim() || amountCents == null
              }
            >
              {capture.isPending ? "Capturing…" : "Capture"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
