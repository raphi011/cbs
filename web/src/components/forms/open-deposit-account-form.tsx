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
import { useAssetLookup, useOpenDepositAccount } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Opens a demand-deposit account backed by a Liability GL account. Overdraft
// limit defaults to 0 (a hard-decline account); a positive limit lets the
// available balance go that far below zero.
export function OpenDepositAccountForm({ pid }: { pid: string }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  // Pre-filled rather than defaulted: the backend refuses an account with no
  // asset, and the account's asset is fixed once it is open.
  const [asset, setAsset] = useState("EUR");
  const [overdraft, setOverdraft] = useState<number | null>(0);
  const create = useOpenDepositAccount(pid);
  // The typed code's scale, resolved against this bank's own asset registry
  // (assets are book-scoped — see ledger.Book.CreateAsset). Until the code
  // resolves to a registered asset there is no scale to convert a non-zero
  // overdraft limit by, so the amount input stays disabled rather than
  // guessing one (0 is scale-invariant, so the default needs no resolution).
  const { byCode } = useAssetLookup(pid);
  const resolvedAsset = byCode.get(asset.trim());

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !asset.trim()) return;
    try {
      const acct = await create.mutateAsync({
        name: name.trim(),
        asset: asset.trim(),
        overdraftLimit: overdraft ?? 0,
      });
      toast.success(`Opened ${acct.name}`);
      setName("");
      setOverdraft(0);
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
            <FieldLabel htmlFor="dda-asset" required>
              Asset
            </FieldLabel>
            <Input
              id="dda-asset"
              value={asset}
              onChange={(e) => setAsset(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              A customer holding two assets holds two accounts, each with its
              own IBAN.
            </p>
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="dda-overdraft" hint="overdraft">
              Overdraft limit
            </FieldLabel>
            <MoneyInput
              id="dda-overdraft"
              value={overdraft}
              onChange={setOverdraft}
              asset={resolvedAsset ?? { code: asset.trim() || "?", scale: 2 }}
              disabled={!resolvedAsset}
            />
            {!resolvedAsset && (
              <p className="text-xs text-muted-foreground">
                Amount disabled until &ldquo;{asset.trim() || "…"}&rdquo; resolves to an asset
                registered in this bank&apos;s book.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={create.isPending || !name.trim() || !asset.trim()}
            >
              {create.isPending ? "Opening…" : "Open account"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
