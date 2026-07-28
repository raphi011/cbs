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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { FieldLabel } from "@/components/field-label";
import { MoneyInput } from "@/components/money";
import { AssetPicker } from "@/components/pickers/asset-picker";
import { useAssetLookup, useOpenFacility } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { parseRatePercent } from "@/lib/rate";
import type { FacilityKind } from "@/lib/enums";

const KIND_OPTIONS: { value: FacilityKind; label: string }[] = [
  { value: "TermLoan", label: "Term loan" },
  { value: "RevolvingLine", label: "Revolving line" },
];

const DAY_COUNT_OPTIONS = ["ACT/365", "ACT/360", "30/360"];

const METHOD_OPTIONS = [
  { value: "Annuity", label: "Annuity (level payment)" },
  { value: "EqualPrincipal", label: "Equal principal" },
];

// Opens a term loan or a revolving line. Both products share a name, an
// asset, a commitment (the term loan's principal, or the line's limit), an
// annual rate and a day-count convention; a term loan additionally needs its
// amortization method and term, and a revolving line its minimum-payment
// fraction — see api/dto_lending.go's openFacilityRequest.
//
// Opening only creates the facility, at Pending: nothing is disbursed or
// drawn here, so commitment/drawn/accrued all start at 0 (see
// api/handlers_lending.go's handleOpenFacility).
export function OpenFacilityForm({ pid }: { pid: string }) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<FacilityKind>("TermLoan");
  const [name, setName] = useState("");
  const [asset, setAsset] = useState("");
  const [commitment, setCommitment] = useState<number | null>(null);
  const [ratePercent, setRatePercent] = useState("");
  const [dayCount, setDayCount] = useState("ACT/365");
  const [method, setMethod] = useState("Annuity");
  const [termMonths, setTermMonths] = useState("");
  const [minPaymentPercent, setMinPaymentPercent] = useState("");
  const create = useOpenFacility(pid);
  const { byCode } = useAssetLookup();
  const resolvedAsset = byCode.get(asset);

  // Same rule OpenDepositAccountForm follows: switching the asset discards a
  // previously-typed commitment rather than reinterpreting its minor units at
  // the new scale.
  function chooseAsset(code: string) {
    setAsset(code);
    setCommitment(null);
  }

  function reset() {
    setKind("TermLoan");
    setName("");
    setAsset("");
    setCommitment(null);
    setRatePercent("");
    setDayCount("ACT/365");
    setMethod("Annuity");
    setTermMonths("");
    setMinPaymentPercent("");
  }

  const rate = parseRatePercent(ratePercent);
  const term = Number.parseInt(termMonths, 10);
  const minPayment = parseRatePercent(minPaymentPercent);

  const ready =
    name.trim() !== "" &&
    asset !== "" &&
    commitment != null &&
    commitment > 0 &&
    rate != null &&
    rate > 0 &&
    (kind === "TermLoan"
      ? Number.isInteger(term) && term > 0
      : minPayment != null && minPayment > 0);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ready || commitment == null || rate == null) return;
    try {
      const f = await create.mutateAsync({
        kind,
        name: name.trim(),
        asset,
        commitment,
        rate,
        dayCount,
        ...(kind === "TermLoan"
          ? { method, termMonths: term }
          : { minPayment: minPayment ?? undefined }),
      });
      toast.success(`Opened ${f.name}`);
      reset();
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
          Open facility
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Open credit facility</DialogTitle>
            <DialogDescription>
              A term loan (fixed principal, disbursed once) or a revolving
              line (a reusable limit, drawn and repaid repeatedly).
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-2">
            <FieldLabel htmlFor="fac-kind" hint="credit-facility" required>
              Kind
            </FieldLabel>
            <Select
              value={kind}
              onValueChange={(v) => setKind(v as FacilityKind)}
            >
              <SelectTrigger id="fac-kind" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {KIND_OPTIONS.map((k) => (
                  <SelectItem key={k.value} value={k.value}>
                    {k.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <FieldLabel htmlFor="fac-name" required>
              Name
            </FieldLabel>
            <Input
              id="fac-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>

          <div className="space-y-2">
            <FieldLabel htmlFor="fac-asset" required>
              Asset
            </FieldLabel>
            <AssetPicker id="fac-asset" value={asset} onChange={chooseAsset} />
          </div>

          <div className="space-y-2">
            <FieldLabel htmlFor="fac-commitment" required>
              {kind === "TermLoan" ? "Principal" : "Commitment (limit)"}
            </FieldLabel>
            {resolvedAsset ? (
              <MoneyInput
                id="fac-commitment"
                value={commitment}
                onChange={setCommitment}
                asset={resolvedAsset}
              />
            ) : (
              <p className="text-xs text-muted-foreground">
                Choose an asset first — an amount has no meaning until its
                scale is known.
              </p>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <FieldLabel htmlFor="fac-rate" required>
                Annual rate (%)
              </FieldLabel>
              <Input
                id="fac-rate"
                inputMode="decimal"
                placeholder="15"
                value={ratePercent}
                onChange={(e) => setRatePercent(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel htmlFor="fac-daycount" required>
                Day count
              </FieldLabel>
              <Select value={dayCount} onValueChange={setDayCount}>
                <SelectTrigger id="fac-daycount" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DAY_COUNT_OPTIONS.map((dc) => (
                    <SelectItem key={dc} value={dc}>
                      {dc}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {kind === "TermLoan" ? (
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-2">
                <FieldLabel htmlFor="fac-method" required>
                  Amortization method
                </FieldLabel>
                <Select value={method} onValueChange={setMethod}>
                  <SelectTrigger id="fac-method" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {METHOD_OPTIONS.map((m) => (
                      <SelectItem key={m.value} value={m.value}>
                        {m.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <FieldLabel htmlFor="fac-term" required>
                  Term (months)
                </FieldLabel>
                <Input
                  id="fac-term"
                  inputMode="numeric"
                  placeholder="60"
                  value={termMonths}
                  onChange={(e) => setTermMonths(e.target.value)}
                />
              </div>
            </div>
          ) : (
            <div className="space-y-2">
              <FieldLabel htmlFor="fac-minpayment" required>
                Minimum payment (% of drawn balance)
              </FieldLabel>
              <Input
                id="fac-minpayment"
                inputMode="decimal"
                placeholder="2"
                value={minPaymentPercent}
                onChange={(e) => setMinPaymentPercent(e.target.value)}
              />
            </div>
          )}

          <DialogFooter>
            <Button type="submit" disabled={create.isPending || !ready}>
              {create.isPending ? "Opening…" : "Open facility"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
