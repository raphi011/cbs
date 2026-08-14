"use client";

import { useState } from "react";
import { ArrowDownLeft, ArrowUpRight } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { MessageDialog } from "@/components/message/message-dialog";
import { formatBusinessDate } from "@/lib/dates";
import { formatSize, shortDefinition, type LogHolder } from "@/lib/message";
import { codeOf } from "@/lib/network-graph";
import { useClearingHouseMessages } from "@/lib/api/hooks";
import { cn } from "@/lib/utils";
import type { Message } from "@/lib/types";

// The files that carried one payment, out of the clearing house's own log, and
// the viewer that opens one.
//
// It sits BESIDE the trail and is not folded into it. A step is a decision this
// institution took and a file is what crossed a wire; nothing records which
// file produced which decision, and within a business day there is no
// chronology to infer one from — every row of a day carries the same instant.
// So the two are shown together and neither claims the other's order.

// This console is the clearing house's, so the log it reads is that
// institution's and there is nothing to resolve: naming the holder from the
// mesh would make a persona's own screen depend on the deployment's read of
// every institution, which is the leak the mesh's placement avoids.
const CLEARING_HOUSE: LogHolder = { kind: "clearing house" };

export function PaymentDocuments({ payid }: { payid: string }) {
  const { data, isLoading, error, refetch } = useClearingHouseMessages({ payment: payid });
  const [open, setOpen] = useState<number | null>(null);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          Files
          <Hint id="bulk-file" />
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {error ? (
          <ErrorState error={error} onRetry={() => void refetch()} />
        ) : isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : !data || data.length === 0 ? (
          // One thing makes a log empty and it is not a defect: a payment its own
          // bank is still holding has been instructed and not yet sent. Every
          // payment in this deployment was submitted through a bank and carried in
          // a file, so there is no second case to distinguish.
          <p className="text-sm text-muted-foreground">
            No file the clearing house has sent or received names this payment. It is still in
            the submitting bank&apos;s hub, waiting for that bank&apos;s next cut-off to carry
            it.
          </p>
        ) : (
          <ul className="divide-y">
            {data.map((m) => (
              <MessageRow key={m.seq} message={m} onOpen={() => setOpen(m.seq)} />
            ))}
          </ul>
        )}
        <MessageDialog holder={CLEARING_HOUSE} seq={open} onClose={() => setOpen(null)} />
      </CardContent>
    </Card>
  );
}

// One file, named the way a banker reads it: what the document is, which way it
// went and who the other end was. A file carries many payments, so the count is
// what says this one was not alone in it.
function MessageRow({ message: m, onOpen }: { message: Message; onOpen: () => void }) {
  const sent = m.direction === "sent";
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        className={cn(
          "flex w-full items-baseline gap-2 rounded-md px-1.5 py-2 text-left",
          "transition-colors hover:bg-accent hover:text-accent-foreground",
        )}
      >
        {sent ? (
          <ArrowUpRight className="size-3.5 shrink-0 translate-y-0.5 text-muted-foreground" />
        ) : (
          <ArrowDownLeft className="size-3.5 shrink-0 translate-y-0.5 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1 space-y-0.5">
          <span className="flex items-baseline gap-1.5">
            <span className="font-mono text-xs" title={m.msgDefIdr}>
              {shortDefinition(m.msgDefIdr)}
            </span>
            <span className="truncate text-xs text-muted-foreground">
              {sent ? "sent to" : "received from"} {codeOf(m.counterparty)}
            </span>
          </span>
          <span className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
            <span className="tabular-nums">{formatSize(m.payloadSize)}</span>
            <span className="tabular-nums">
              {m.payments.length} {m.payments.length === 1 ? "payment" : "payments"}
            </span>
            <span>{formatBusinessDate(m.at.slice(0, 10))}</span>
          </span>
        </span>
      </button>
    </li>
  );
}
