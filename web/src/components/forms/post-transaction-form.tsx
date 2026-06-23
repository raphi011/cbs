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
import { GLAccountPicker } from "@/components/pickers/gl-account-picker";
import { Hint } from "@/components/hint";
import { usePostTransaction } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { newIdempotencyKey } from "@/lib/idempotency";
import type { Direction } from "@/lib/enums";
import { cn } from "@/lib/utils";

interface Leg {
  accountId: string;
  direction: Direction;
  cents: number | null;
}

const emptyLegs = (): Leg[] => [
  { accountId: "", direction: "Debit", cents: null },
  { accountId: "", direction: "Credit", cents: null },
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

  const debits = legs
    .filter((l) => l.direction === "Debit")
    .reduce((s, l) => s + (l.cents ?? 0), 0);
  const credits = legs
    .filter((l) => l.direction === "Credit")
    .reduce((s, l) => s + (l.cents ?? 0), 0);
  const balanced = debits === credits && debits > 0;
  const ready =
    balanced && legs.every((l) => l.accountId.trim() && (l.cents ?? 0) > 0);

  function reset() {
    setLegs(emptyLegs());
    setIdempotencyKey(newIdempotencyKey());
    setDescription("");
    setBookingDate("");
    setValueDate("");
  }

  function updateLeg(i: number, patch: Partial<Leg>) {
    setLegs((prev) => prev.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ready) return;
    post.mutate(
      {
        idempotencyKey,
        entries: legs.map((l) => ({
          accountId: l.accountId.trim(),
          amount: l.cents ?? 0,
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
              Add legs until total debits equal total credits. Amounts are in
              euros; they’re stored as integer cents.
            </DialogDescription>
          </DialogHeader>

          {/* Legs */}
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium">Legs</span>
              <Hint id="amount-cents" />
            </div>
            {legs.map((leg, i) => (
              <div key={i} className="flex items-end gap-2">
                <div className="min-w-0 flex-1">
                  <GLAccountPicker
                    pid={pid}
                    value={leg.accountId}
                    onChange={(accountId) => updateLeg(i, { accountId })}
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
                  <MoneyInput
                    valueCents={leg.cents}
                    onChangeCents={(c) => updateLeg(i, { cents: c })}
                  />
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
            ))}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                setLegs((prev) => [
                  ...prev,
                  { accountId: "", direction: "Credit", cents: null },
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
              Debits <Money cents={debits} /> · Credits{" "}
              <Money cents={credits} />
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
