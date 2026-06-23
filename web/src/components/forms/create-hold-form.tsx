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
import { Input } from "@/components/ui/input";
import { FieldLabel } from "@/components/field-label";
import { MoneyInput } from "@/components/money";
import { useCreateHold } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Places an authorization hold: reserves funds so they drop out of the
// available balance without touching the book balance or the ledger.
export function CreateHoldForm({ pid, did }: { pid: string; did: string }) {
  const [open, setOpen] = useState(false);
  const [amountCents, setAmountCents] = useState<number | null>(null);
  const [expires, setExpires] = useState("");
  const [description, setDescription] = useState("");
  const create = useCreateHold(pid, did);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (amountCents == null) return;
    try {
      const hold = await create.mutateAsync({
        amount: amountCents,
        // *time.Time wants RFC3339; <input type="date"> gives YYYY-MM-DD.
        expiresAt: expires ? `${expires}T00:00:00.000Z` : null,
        description: description.trim() || undefined,
      });
      toast.success(`Hold placed (${hold.id})`);
      setAmountCents(null);
      setExpires("");
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
          <Plus className="size-4" />
          New hold
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Place a hold</DialogTitle>
            <DialogDescription>
              Reserves funds (the available balance drops; the book balance is
              unchanged). Capture it later for the final amount, or release it.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="hold-amount" hint="holds" required>
              Amount
            </FieldLabel>
            <MoneyInput
              id="hold-amount"
              valueCents={amountCents}
              onChangeCents={setAmountCents}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="hold-expires">Expires (optional)</FieldLabel>
            <Input
              id="hold-expires"
              type="date"
              value={expires}
              onChange={(e) => setExpires(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="hold-desc">Description (optional)</FieldLabel>
            <Input
              id="hold-desc"
              value={description}
              placeholder="e.g. card authorization at merchant"
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={create.isPending || amountCents == null}
            >
              {create.isPending ? "Placing…" : "Place hold"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
