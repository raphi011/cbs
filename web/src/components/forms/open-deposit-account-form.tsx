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
import { AssetPicker } from "@/components/pickers/asset-picker";
import { ProductPicker } from "@/components/pickers/product-picker";
import { useAssetLookup, useOpenDepositAccount } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Opens a demand-deposit account, whose money pools in the bank's customer-
// deposit control account for its asset rather than in a line of its own. Overdraft
// limit defaults to 0 (a hard-decline account); a positive limit lets the
// available balance go that far below zero.
//
// The account is opened FROM a catalogue product, chosen here. Its PRICE comes
// from that product rather than from this form: the limit is asked for and the
// rate is not, which is the pinned/floating distinction made visible — a limit
// is an underwriting decision about this customer, a rate is what the bank
// charges for the product. A later published version reprices this account
// without anyone touching it.
export function OpenDepositAccountForm({ pid }: { pid: string }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  // No default: the backend refuses an account with no asset, and the
  // account's asset is fixed once it is open.
  const [asset, setAsset] = useState("");
  const [overdraft, setOverdraft] = useState<number | null>(null);
  const [productId, setProductId] = useState("");
  const create = useOpenDepositAccount(pid);
  // Until an asset is chosen there is no scale to convert a typed overdraft
  // limit by, so the amount input is not rendered at all rather than guessing.
  const { byCode } = useAssetLookup();
  const resolvedAsset = byCode.get(asset);

  // Switching the asset discards a previously-typed limit rather than
  // reinterpreting its minor units at the new scale: 5.00 EUR is 500, and 500
  // under a scale-8 asset is 0.000005, a number the user never asked for.
  // Same rule as a transaction leg switching accounts in
  // post-transaction-form.
  function chooseAsset(code: string) {
    setAsset(code);
    setOverdraft(null);
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !asset || !productId) return;
    try {
      const acct = await create.mutateAsync({
        name: name.trim(),
        asset,
        productId,
        overdraftLimit: overdraft ?? 0,
      });
      toast.success(`Opened ${acct.name}`);
      setName("");
      setAsset("");
      setProductId("");
      setOverdraft(null);
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
              A customer checking/current account. It gets no line of its own in
              the chart of accounts: it becomes an obligor under the bank&apos;s
              customer-deposit control account.
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
            <FieldLabel htmlFor="dda-product" required>
              Product
            </FieldLabel>
            <ProductPicker
              pid={pid}
              id="dda-product"
              value={productId}
              onChange={setProductId}
            />
            <p className="text-xs text-muted-foreground">
              The account is priced by this product. Publishing a new version of
              it reprices every account on it, without touching any of them.
            </p>
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="dda-asset" required>
              Asset
            </FieldLabel>
            <AssetPicker id="dda-asset" value={asset} onChange={chooseAsset} />
            <p className="text-xs text-muted-foreground">
              A customer holding two assets holds two accounts, each with its
              own IBAN.
            </p>
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="dda-overdraft" hint="overdraft">
              Overdraft limit
            </FieldLabel>
            {resolvedAsset ? (
              <MoneyInput
                id="dda-overdraft"
                value={overdraft}
                onChange={setOverdraft}
                asset={resolvedAsset}
              />
            ) : (
              <p className="text-xs text-muted-foreground">
                Choose an asset first — an amount has no meaning until its scale
                is known. Left empty, the limit is 0: a hard-decline account.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={create.isPending || !name.trim() || !asset}
            >
              {create.isPending ? "Opening…" : "Open account"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
