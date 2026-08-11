"use client";

import { useState } from "react";
import { Plus, RefreshCw, Trash2 } from "lucide-react";
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
import { MoneyInput, Money } from "@/components/money";
import { FieldLabel } from "@/components/field-label";
import { PositionPicker } from "@/components/pickers/position-picker";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { useAllAccounts, useAssetLookup, usePostTransaction } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { newIdempotencyKey } from "@/lib/idempotency";
import type { Direction } from "@/lib/enums";
import { cn } from "@/lib/utils";

interface Leg {
  accountId: string;
  // Whose money this leg is, when the account it posts to pools subsidiaries. The
  // server refuses a leg against a control account that names nobody, and one
  // against a plain account that names somebody — so this field appears exactly
  // when the picked account says it must.
  subsidiary: string;
  direction: Direction;
  amount: number | null;
}

const emptyLegs = (): Leg[] => [
  { accountId: "", subsidiary: "", direction: "Debit", amount: null },
  { accountId: "", subsidiary: "", direction: "Credit", amount: null },
];

function dateToRFC3339(date: string): string | null {
  // <input type="date"> yields "YYYY-MM-DD"; the API expects RFC3339, so pin
  // it to midnight UTC.
  return date ? `${date}T00:00:00.000Z` : null;
}

export function PostTransactionForm({ pid }: { pid: string }) {
  const [open, setOpen] = useState(false);
  const [legs, setLegs] = useState<Leg[]>(emptyLegs);
  const [idempotencyKey, setIdempotencyKey] = useState(newIdempotencyKey);
  const [description, setDescription] = useState("");
  const [bookingDate, setBookingDate] = useState("");
  const [valueDate, setValueDate] = useState("");
  const post = usePostTransaction(pid);

  // A leg's amount is denominated in the asset of the account it posts to, and
  // legs of one transaction can balance in different assets — so each leg's
  // scale is resolved from its own picked account rather than assumed shared
  // across the form.
  const accounts = useAllAccounts(pid);
  const { byCode } = useAssetLookup();
  function assetForLeg(accountId: string) {
    const acct = accounts.data?.find((a) => a.id === accountId);
    return acct ? byCode.get(acct.asset) : undefined;
  }
  function poolsSubsidiaries(accountId: string) {
    return accounts.data?.find((a) => a.id === accountId)?.control ?? false;
  }

  const debits = legs
    .filter((l) => l.direction === "Debit")
    .reduce((s, l) => s + (l.amount ?? 0), 0);
  const credits = legs
    .filter((l) => l.direction === "Credit")
    .reduce((s, l) => s + (l.amount ?? 0), 0);

  // The debits/credits summary below is only meaningful rendered in one
  // asset — a raw sum across different assets isn't a number of anything, per
  // the same reasoning as the home page's reserve totals. Only show it once
  // every leg's account has resolved to the same asset.
  const legAssets = legs.map((l) => assetForLeg(l.accountId));
  const summaryAsset =
    legAssets.length > 0 && legAssets.every((a) => a && a.code === legAssets[0]?.code)
      ? legAssets[0]
      : undefined;

  // Balance is per asset, exactly as the server checks it (ledger's
  // validateBalance / ErrUnbalancedAsset). A global debits === credits is
  // satisfied by 100 cents against 100 satoshi, which is the bug chapter 16
  // exists to teach against — showing "Balanced ✓" for it would demonstrate
  // the mistake in the app that teaches it. A leg whose account has not
  // resolved yet has no asset, so nothing can be said to balance.
  const netByAsset = new Map<string, number>();
  let everyLegResolved = legs.length > 0;
  legs.forEach((l, i) => {
    const code = legAssets[i]?.code;
    if (!code) {
      everyLegResolved = false;
      return;
    }
    const signed = (l.amount ?? 0) * (l.direction === "Debit" ? 1 : -1);
    netByAsset.set(code, (netByAsset.get(code) ?? 0) + signed);
  });
  const balanced =
    everyLegResolved &&
    debits > 0 &&
    [...netByAsset.values()].every((net) => net === 0);
  // A leg against a control account with no subsidiary is refused by the server,
  // so the form refuses it first: the alternative is a 400 the user cannot read
  // the cause of off a form that looked complete.
  const ready =
    balanced &&
    legs.every(
      (l) =>
        l.accountId.trim() &&
        (l.amount ?? 0) > 0 &&
        (!poolsSubsidiaries(l.accountId) || l.subsidiary.trim() !== ""),
    );

  function reset() {
    setLegs(emptyLegs());
    setIdempotencyKey(newIdempotencyKey());
    setDescription("");
    setBookingDate("");
    setValueDate("");
  }

  function updateLeg(i: number, patch: Partial<Leg>) {
    setLegs((prev) =>
      prev.map((l, idx) => {
        if (idx !== i) return l;
        // Switching accounts can switch assets, so a previously-typed amount
        // (minor units at the old account's scale) is discarded rather than
        // silently reinterpreted at the new account's scale.
        const next = { ...l, ...patch };
        if (patch.accountId !== undefined && patch.accountId !== l.accountId) {
          next.amount = null;
        }
        return next;
      }),
    );
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ready) return;
    post.mutate(
      {
        idempotencyKey,
        entries: legs.map((l) => ({
          accountId: l.accountId.trim(),
          // Omitted rather than empty on a plain account: the wire's absence is
          // "the whole account", and an empty string would be the same thing
          // said twice.
          subsidiary: l.subsidiary.trim() || undefined,
          amount: l.amount ?? 0,
          direction: l.direction,
        })),
        bookingDate: dateToRFC3339(bookingDate),
        valueDate: dateToRFC3339(valueDate),
        description: description.trim(),
        metadata: null,
      },
      {
        onSuccess: (tx) => {
          toast.success(`Posted transaction ${tx.id}`);
          reset();
          setOpen(false);
        },
        onError: (err) => toast.error(describeError(err)),
      },
    );
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (o) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus className="size-4" />
          Post transaction
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              Post transaction
              <Hint id="double-entry" />
            </DialogTitle>
            <DialogDescription>
              Add legs until total debits equal total credits per asset. Each
              leg&apos;s amount is in the asset of the account it posts to,
              stored as an integer at that asset&apos;s scale.
            </DialogDescription>
          </DialogHeader>

          {/* Legs */}
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium">Legs</span>
              <Hint id="minor-units" />
            </div>
            {legs.map((leg, i) => {
              const asset = assetForLeg(leg.accountId);
              return (
              <div key={i} className="flex items-end gap-2">
                <div className="min-w-0 flex-1">
                  <PositionPicker
                    pid={pid}
                    value={{ account: leg.accountId, subsidiary: leg.subsidiary }}
                    onChange={(pos) =>
                      updateLeg(i, { accountId: pos.account, subsidiary: pos.subsidiary })
                    }
                  />
                </div>
                <Select
                  value={leg.direction}
                  onValueChange={(v) =>
                    updateLeg(i, { direction: v as Direction })
                  }
                >
                  <SelectTrigger className="w-[110px]" size="sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="Debit">Debit</SelectItem>
                    <SelectItem value="Credit">Credit</SelectItem>
                  </SelectContent>
                </Select>
                <div className="w-32">
                  {asset ? (
                    <MoneyInput
                      value={leg.amount}
                      onChange={(a) => updateLeg(i, { amount: a })}
                      asset={asset}
                    />
                  ) : (
                    <Skeleton className="h-9 w-full" />
                  )}
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  disabled={legs.length <= 2}
                  onClick={() =>
                    setLegs((prev) => prev.filter((_, idx) => idx !== i))
                  }
                  aria-label="Remove leg"
                >
                  <Trash2 className="size-4" />
                </Button>
              </div>
              );
            })}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                setLegs((prev) => [
                  ...prev,
                  { accountId: "", subsidiary: "", direction: "Credit", amount: null },
                ])
              }
            >
              <Plus className="size-4" />
              Add leg
            </Button>
          </div>

          {/* Balance indicator */}
          <div
            className={cn(
              "flex items-center justify-between rounded-md border px-3 py-2 text-sm",
              balanced
                ? "border-emerald-500/40 bg-emerald-500/5"
                : "border-amber-500/40 bg-amber-500/5",
            )}
          >
            <span className="text-muted-foreground">
              {summaryAsset ? (
                <>
                  Debits <Money amount={debits} asset={summaryAsset} /> · Credits{" "}
                  <Money amount={credits} asset={summaryAsset} />
                </>
              ) : (
                <>
                  Legs are in different assets — each one balances on its own,
                  so there is no single total. See each leg above.
                </>
              )}
            </span>
            <span className={balanced ? "text-emerald-600" : "text-amber-600"}>
              {balanced ? "Balanced ✓" : "Not balanced"}
            </span>
          </div>

          {/* Metadata */}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tx-booking" hint="booking-date">
                Booking date
              </FieldLabel>
              <Input
                id="tx-booking"
                type="date"
                value={bookingDate}
                onChange={(e) => setBookingDate(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tx-value" hint="value-date">
                Value date
              </FieldLabel>
              <Input
                id="tx-value"
                type="date"
                value={valueDate}
                onChange={(e) => setValueDate(e.target.value)}
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <FieldLabel htmlFor="tx-desc">Description</FieldLabel>
            <Input
              id="tx-desc"
              value={description}
              placeholder="optional"
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>

          <div className="space-y-1.5">
            <FieldLabel hint="idempotency-key">Idempotency key</FieldLabel>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded bg-muted px-2 py-1.5 text-xs">
                {idempotencyKey}
              </code>
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label="Regenerate key"
                onClick={() => setIdempotencyKey(newIdempotencyKey())}
              >
                <RefreshCw className="size-4" />
              </Button>
            </div>
          </div>

          <DialogFooter>
            <Button type="submit" disabled={!ready || post.isPending}>
              {post.isPending ? "Posting…" : "Post transaction"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
