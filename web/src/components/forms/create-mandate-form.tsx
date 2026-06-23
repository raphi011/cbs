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
import { useCreateMandate } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { PartyRef } from "@/lib/types";

// A mandate is standing authorization for a creditor to pull funds from a
// debtor, up to a maximum amount — the prerequisite for pull (direct-debit)
// schemes.
export function CreateMandateForm() {
  const [open, setOpen] = useState(false);
  const [debtor, setDebtor] = useState<PartyRef>(emptyPartyRef);
  const [creditor, setCreditor] = useState<PartyRef>(emptyPartyRef);
  const [maxCents, setMaxCents] = useState<number | null>(null);
  const create = useCreateMandate();

  const valid =
    debtor.participant.trim() &&
    debtor.account.trim() &&
    creditor.participant.trim() &&
    creditor.account.trim() &&
    maxCents != null;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!valid) return;
    try {
      const m = await create.mutateAsync({
        debtor,
        creditor,
        maxAmount: maxCents!,
      });
      toast.success(`Mandate created (${m.id})`);
      setDebtor(emptyPartyRef);
      setCreditor(emptyPartyRef);
      setMaxCents(null);
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
            <MoneyInput
              id="mandate-max"
              valueCents={maxCents}
              onChangeCents={setMaxCents}
            />
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
