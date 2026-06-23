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
import { useOpenDepositAccount } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Opens a demand-deposit account backed by a Liability GL account. Overdraft
// limit defaults to 0 (a hard-decline account); a positive limit lets the
// available balance go that far below zero.
export function OpenDepositAccountForm({ pid }: { pid: string }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [overdraftCents, setOverdraftCents] = useState<number | null>(0);
  const create = useOpenDepositAccount(pid);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    try {
      const acct = await create.mutateAsync({
        name: name.trim(),
        overdraftLimit: overdraftCents ?? 0,
      });
      toast.success(`Opened ${acct.name}`);
      setName("");
      setOverdraftCents(0);
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
          Open account
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Open deposit account</DialogTitle>
            <DialogDescription>
              A customer checking/current account, backed by a Liability GL
              account in the general ledger.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="dda-name" required>
              Account holder name
            </FieldLabel>
            <Input
              id="dda-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="dda-overdraft" hint="overdraft">
              Overdraft limit
            </FieldLabel>
            <MoneyInput
              id="dda-overdraft"
              valueCents={overdraftCents}
              onChangeCents={setOverdraftCents}
            />
          </div>
          <DialogFooter>
            <Button type="submit" disabled={create.isPending || !name.trim()}>
              {create.isPending ? "Opening…" : "Open account"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
