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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FieldLabel } from "@/components/field-label";
import { MoneyInput } from "@/components/money";
import { PartyRefFields, emptyPartyRef } from "@/components/forms/party-ref-fields";
import { useInitiatePayment, useSchemes } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { PartyRef } from "@/lib/types";

// schemeDTO carries no asset field, even though the API resolves one
// server-side for the payment it produces (see toPaymentDTO in
// api/dto_payment.go) — so the chosen scheme can't tell this form its scale
// either. Every scheme implemented so far settles in EUR (see
// payment/scheme.go's SCT/SDD) — see the matching comment in
// net-positions-table.tsx.
const PAYMENT_ASSET = { code: "EUR", scale: 2 };

// Initiates a payment under a scheme. The form is scheme-aware: a mandate is
// required only when the chosen scheme requires one (pull/direct-debit).
export function InitiatePaymentForm() {
  const [open, setOpen] = useState(false);
  const schemes = useSchemes();
  const [scheme, setScheme] = useState("");
  const [debtor, setDebtor] = useState<PartyRef>(emptyPartyRef);
  const [creditor, setCreditor] = useState<PartyRef>(emptyPartyRef);
  const [amount, setAmount] = useState<number | null>(null);
  const [mandateId, setMandateId] = useState("");
  const [endToEndId, setEndToEndId] = useState("");
  const [description, setDescription] = useState("");
  const initiate = useInitiatePayment();

  const selected = schemes.data?.find((s) => s.id === scheme);
  const needsMandate = selected?.requiresMandate ?? false;

  const valid =
    scheme &&
    debtor.participant.trim() &&
    debtor.account.trim() &&
    creditor.participant.trim() &&
    creditor.account.trim() &&
    amount != null &&
    (!needsMandate || mandateId.trim());

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!valid) return;
    try {
      const p = await initiate.mutateAsync({
        scheme,
        debtor,
        creditor,
        amount: amount!,
        mandateId: mandateId.trim() || undefined,
        endToEndId: endToEndId.trim() || undefined,
        description: description.trim() || undefined,
      });
      toast.success(`Payment initiated (${p.id})`);
      setDebtor(emptyPartyRef);
      setCreditor(emptyPartyRef);
      setAmount(null);
      setMandateId("");
      setEndToEndId("");
      setDescription("");
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
          Initiate payment
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Initiate payment</DialogTitle>
            <DialogDescription>
              Money always flows debtor → creditor. The scheme sets the rules
              (direction, mandate requirement, settlement model).
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-2">
            <FieldLabel htmlFor="pay-scheme" hint="payment-lifecycle" required>
              Scheme
            </FieldLabel>
            <Select value={scheme} onValueChange={setScheme}>
              <SelectTrigger id="pay-scheme">
                <SelectValue placeholder="Choose a scheme…" />
              </SelectTrigger>
              <SelectContent>
                {schemes.data?.map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.id} · {s.direction}
                    {s.requiresMandate ? " · mandate" : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <PartyRefFields legend="Debtor" value={debtor} onChange={setDebtor} />
          <PartyRefFields
            legend="Creditor"
            value={creditor}
            onChange={setCreditor}
          />

          <div className="space-y-2">
            <FieldLabel htmlFor="pay-amount" required>
              Amount
            </FieldLabel>
            <MoneyInput
              id="pay-amount"
              value={amount}
              onChange={setAmount}
              asset={PAYMENT_ASSET}
            />
          </div>

          {needsMandate && (
            <div className="space-y-2">
              <FieldLabel htmlFor="pay-mandate" hint="requires-mandate" required>
                Mandate
              </FieldLabel>
              <Input
                id="pay-mandate"
                value={mandateId}
                placeholder="mnd_… (this scheme pulls funds)"
                className="font-mono"
                onChange={(e) => setMandateId(e.target.value)}
              />
            </div>
          )}

          <div className="space-y-2">
            <FieldLabel htmlFor="pay-e2e">End-to-end ID (optional)</FieldLabel>
            <Input
              id="pay-e2e"
              value={endToEndId}
              onChange={(e) => setEndToEndId(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="pay-desc">Description (optional)</FieldLabel>
            <Input
              id="pay-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>

          <DialogFooter>
            <Button type="submit" disabled={initiate.isPending || !valid}>
              {initiate.isPending ? "Initiating…" : "Initiate payment"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
