"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Money, MoneyInput } from "@/components/money";
import { DepositAccountPicker } from "@/components/pickers/deposit-account-picker";
import {
  useAssetLookup,
  useDepositAccounts,
  useDepositBalance,
  useTransfer,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { Asset, DepositAccount, Transfer } from "@/lib/types";

// The bank's own side of the book transfer, and the screen that says what makes
// it different from every other way money moves in this console.
//
// It is the one act here with no counterparty: no message goes out, no clearing
// cycle takes it in, no reserve moves and no statement comes back. Both accounts
// are in this bank's register, which is why this bank may post both legs at
// once — and why nothing else in the system is allowed to.

// ibanOf is the address the bank minted for an account.
//
// The console can do what a payer cannot — turn an account into its address —
// because it IS the register that issued it. The request still names the
// ADDRESS: the internal account id is this bank's key, and building the request
// out of it would be a different rule for the operator than for the customer.
function ibanOf(account: DepositAccount | undefined): string {
  return account?.identifiers.find((i) => i.scheme === "IBAN")?.value ?? "";
}

// The payer's spendable figure, which is the number the transfer is measured
// against — not the book balance, because an arranged overdraft is money the
// customer may spend and a transfer may legitimately use it.
function PayerAvailable({ pid, did }: { pid: string; did: string }) {
  const { data, error } = useDepositBalance(pid, did);
  const { byCode } = useAssetLookup();
  const asset = data ? byCode.get(data.asset) : undefined;
  if (error)
    return <span className="text-destructive">{describeError(error)}</span>;
  if (!data || !asset) return <Skeleton className="h-4 w-16" />;
  return <Money amount={data.available} asset={asset} />;
}

export default function BankTransfersPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";

  const { data: accounts, error, isLoading } = useDepositAccounts(pid);
  const { byCode } = useAssetLookup();
  const move = useTransfer(pid);

  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [amount, setAmount] = useState<number | null>(null);
  const [description, setDescription] = useState("");
  // The asset is captured with the receipt rather than looked up when the alert
  // renders: a number with no scale is not an amount, and the form clears itself
  // on success, so by then there is no account left to read one off.
  const [moved, setMoved] = useState<{
    receipt: Transfer;
    amount: number;
    asset: Asset;
  } | null>(null);

  const payer = accounts?.find((a) => a.id === from);
  const payee = accounts?.find((a) => a.id === to);
  const asset = payer ? byCode.get(payer.asset) : undefined;
  const payeeAddress = ibanOf(payee);

  // Three refusals stated before the request rather than after it. Each is one
  // the bank would make anyway; showing them here is what stops an operator
  // reading a 400 and wondering which field it meant.
  const sameAccount = from !== "" && from === to;
  const assetMismatch =
    payer != null && payee != null && payer.asset !== payee.asset;
  const unaddressable = payee != null && payeeAddress === "";

  const canMove =
    asset != null &&
    from !== "" &&
    to !== "" &&
    !sameAccount &&
    !assetMismatch &&
    !unaddressable &&
    amount != null &&
    amount > 0;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!canMove || asset == null) return;
    try {
      const receipt = await move.mutateAsync({
        from,
        to: payeeAddress,
        amount: amount!,
        description: description.trim() || undefined,
      });
      setMoved({ receipt, amount: amount!, asset });
      setAmount(null);
      setDescription("");
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  if (error) return <ErrorState error={error} />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Book transfer"
        hint="book-transfer"
        description="Money between two of this bank's own customers. Nothing crosses between institutions, so there is no position to clear, no reserves to move and nobody to tell — this bank posts both legs itself, in one transaction, and the act is over when it returns."
      />

      {moved && (
        <Alert>
          <AlertTitle>Posted</AlertTitle>
          <AlertDescription className="space-y-2">
            <span>
              <Money amount={moved.amount} asset={moved.asset} />{" "}
              moved as <IdText id={moved.receipt.transactionId} />{" "}
              — one transaction with both legs on it. The payer&apos;s book
              balance is now{" "}
              <Money amount={moved.receipt.balance.book} asset={moved.asset} />.
            </span>
            <span className="flex gap-3">
              <Link
                href={`/bank/${pid}/deposit-audit`}
                className="text-xs underline underline-offset-2"
              >
                See it on the deposit audit
              </Link>
              <button
                type="button"
                onClick={() => setMoved(null)}
                className="text-xs underline underline-offset-2"
              >
                Dismiss
              </button>
            </span>
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-1.5">
              <FieldLabel
                htmlFor="transfer-from"
                hint="balance-available"
                required
              >
                From
              </FieldLabel>
              <DepositAccountPicker
                id="transfer-from"
                pid={pid}
                value={from}
                onChange={setFrom}
              />
              {payer && (
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span>Available</span>
                  <PayerAvailable pid={pid} did={payer.id} />
                  <span>
                    — a frozen or dormant payer is refused outright, and an
                    arranged overdraft is spendable.
                  </span>
                </div>
              )}
            </div>

            <div className="space-y-1.5">
              <FieldLabel
                htmlFor="transfer-to"
                hint="account-addressing"
                required
              >
                To
              </FieldLabel>
              <DepositAccountPicker
                id="transfer-to"
                pid={pid}
                value={to}
                onChange={setTo}
              />
              {sameAccount ? (
                <p className="text-xs text-destructive">
                  That is the account the money is coming from. A transfer needs
                  two — one account named twice would post a self-cancelling
                  pair and leave a record saying money moved.
                </p>
              ) : assetMismatch ? (
                <p className="text-xs text-destructive">
                  These two accounts hold {payer?.asset} and {payee?.asset}. An
                  amount is one number at one scale, so this is not a transfer
                  at a bad rate — converting is a second operation with a price
                  in it, and this bank has no desk that quotes one.
                </p>
              ) : unaddressable ? (
                <p className="text-xs text-destructive">
                  That account holds no IBAN, and the transfer names its payee
                  by address.
                </p>
              ) : payeeAddress !== "" ? (
                <p className="text-xs text-muted-foreground">
                  Named by <span className="font-mono">{payeeAddress}</span> —
                  the address this bank issued it. A payer types the same thing;
                  the console can turn an account into its address because it is
                  the register that minted it. Money lands in a frozen or
                  dormant account and only a closed one refuses it.
                </p>
              ) : null}
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="transfer-amount" required>
                Amount
              </FieldLabel>
              {asset ? (
                <MoneyInput
                  id="transfer-amount"
                  value={amount}
                  onChange={setAmount}
                  asset={asset}
                />
              ) : (
                <Skeleton className="h-9 w-full" />
              )}
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="transfer-desc">
                Description (optional)
              </FieldLabel>
              <Input
                id="transfer-desc"
                value={description}
                placeholder="e.g. rent"
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>

            <Button
              type="submit"
              disabled={!canMove || move.isPending || isLoading}
            >
              {move.isPending ? "Posting…" : "Post transfer"}
            </Button>
          </form>
        </CardContent>
      </Card>

      <p className="max-w-prose text-xs text-muted-foreground">
        There is no list of transfers here, and its absence is the substance: a
        transfer is a ledger transaction and a deposit-layer event, not a
        payment. It gets no row in <span className="font-mono">payments</span> —
        that table carries no <span className="font-mono">cycle_id</span>{" "}
        precisely because clearing belongs to somebody else — so it appears on
        the two logs it genuinely belongs to. See{" "}
        <Link
          href={`/bank/${pid}/deposit-audit`}
          className="underline underline-offset-2"
        >
          deposit audit
        </Link>{" "}
        and{" "}
        <Link
          href={`/bank/${pid}/transactions`}
          className="underline underline-offset-2"
        >
          transactions
        </Link>
        . <Hint id="book-transfer" />
      </p>
    </div>
  );
}
