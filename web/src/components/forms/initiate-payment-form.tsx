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
import {
  PartyRefFields,
  emptyParty,
  type PartyDraft,
} from "@/components/forms/party-ref-fields";
import { useAssetLookup, useInitiatePayment, useSchemes } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Initiates a payment under a scheme. The form is scheme-aware: a mandate is
// required only when the chosen scheme requires one (pull/direct-debit).
//
// Neither side carries a BIC. The counterparty's bank is derived from the
// counterparty's IBAN, through the submitting bank's copy of the scheme's
// routing directory, so this form asks for an address and a name and there is no
// field left to put a bank in. What it sends is both names, because which side
// is the counterparty belongs to the scheme and the backend already knows the
// direction — guessing it a second time here would be a rule in two places.
export function InitiatePaymentForm() {
  const [open, setOpen] = useState(false);
  const schemes = useSchemes();
  const [scheme, setScheme] = useState("");
  const [debtor, setDebtor] = useState<PartyDraft>(emptyParty);
  const [debtorName, setDebtorName] = useState("");
  const [creditor, setCreditor] = useState<PartyDraft>(emptyParty);
  const [creditorName, setCreditorName] = useState("");
  const [amount, setAmount] = useState<number | null>(null);
  const [mandateId, setMandateId] = useState("");
  const [endToEndId, setEndToEndId] = useState("");
  const [description, setDescription] = useState("");
  const initiate = useInitiatePayment();

  const selected = schemes.data?.find((s) => s.id === scheme);
  const needsMandate = selected?.requiresMandate ?? false;

  // schemeDTO carries the scheme's asset (see api/dto_payment.go's
  // toSchemeDTO), the same way a payment's asset is the scheme's. Its scale
  // comes from the network-wide asset list, so the amount input is scaled
  // correctly the moment a scheme is chosen.
  const { byCode } = useAssetLookup();
  const resolvedAsset = selected ? byCode.get(selected.asset) : undefined;

  // The counterparty is whichever side the submitting bank is not, and its NAME
  // is required because nothing else says who is being paid
  // (payment.ErrCounterpartyNotNamed).
  const pull = selected?.direction === "Pull";
  const counterpartyName = pull ? debtorName : creditorName;

  // BOTH addresses are required here, and for two different reasons — which is
  // the shape this console has and a bank's own port does not.
  //
  // The COUNTERPARTY's, because the message quotes an address the submitting
  // bank cannot resolve and because the counterparty's bank is derived from it.
  //
  // The SUBMITTING side's, because an instruction now names no bank at all, so
  // this console has nothing to hand it to until it works out whose act it is —
  // and it does that by resolving the payer's own address (the payee's, on a
  // pull) in the roster it publishes. A bank's own port reads that off the
  // LISTENER instead and needs no address for it. See api's
  // handleInitiatePayment.
  const valid =
    scheme &&
    debtor.pid.trim() &&
    debtor.ref.account.trim() &&
    debtor.ref.identifier?.value.trim() &&
    creditor.pid.trim() &&
    creditor.ref.account.trim() &&
    creditor.ref.identifier?.value.trim() &&
    counterpartyName.trim() &&
    resolvedAsset != null &&
    amount != null &&
    (!needsMandate || mandateId.trim());

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!valid) return;
    try {
      const p = await initiate.mutateAsync({
        scheme,
        debtor: debtor.ref,
        creditor: creditor.ref,
        debtorName: debtorName.trim() || undefined,
        creditorName: creditorName.trim() || undefined,
        amount: amount!,
        mandateId: mandateId.trim() || undefined,
        endToEndId: endToEndId.trim() || undefined,
        description: description.trim() || undefined,
      });
      toast.success(`Payment initiated (${p.id})`);
      setDebtor(emptyParty);
      setDebtorName("");
      setCreditor(emptyParty);
      setCreditorName("");
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

          <PartyRefFields
            legend="Debtor"
            value={debtor}
            onChange={setDebtor}
            name={debtorName}
            onNameChange={setDebtorName}
            addressRequired
          />
          <PartyRefFields
            legend="Creditor"
            value={creditor}
            onChange={setCreditor}
            name={creditorName}
            onNameChange={setCreditorName}
            addressRequired
          />

          <div className="space-y-2">
            <FieldLabel htmlFor="pay-amount" required>
              Amount
            </FieldLabel>
            {resolvedAsset ? (
              <MoneyInput
                id="pay-amount"
                value={amount}
                onChange={setAmount}
                asset={resolvedAsset}
              />
            ) : (
              <p className="text-xs text-muted-foreground">
                Choose a scheme and debtor to resolve the payment&apos;s
                asset.
              </p>
            )}
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
