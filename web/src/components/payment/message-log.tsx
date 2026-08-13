"use client";

import { useState } from "react";
import { ArrowDownLeft, ArrowUpRight } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { CopyButton } from "@/components/copy-button";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { formatBusinessDate } from "@/lib/dates";
import { formatSize, shortDefinition } from "@/lib/message";
import { codeOf } from "@/lib/network-graph";
import { useClearingHouseMessage, useClearingHouseMessages } from "@/lib/api/hooks";
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
          // Two things make a log empty and neither is a defect, so the absence
          // says which rather than promising a file that may never come: a
          // payment the sample dataset placed was never sent by anybody.
          <p className="text-sm text-muted-foreground">
            No file the clearing house has sent or received names this payment. Either its
            bank&apos;s next cut-off has yet to carry it, or it came from the sample dataset,
            which builds a deployment by playing every institution rather than by sending
            anything.
          </p>
        ) : (
          <ul className="divide-y">
            {data.map((m) => (
              <MessageRow key={m.seq} message={m} onOpen={() => setOpen(m.seq)} />
            ))}
          </ul>
        )}
        <MessageDialog seq={open} onClose={() => setOpen(null)} />
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

// The document, as it travelled. It is fetched only when one is asked for: a
// listing leaves every payload unread and carries its size instead, and this is
// the one read in the app that is handed a file's bytes.
function MessageDialog({ seq, onClose }: { seq: number | null; onClose: () => void }) {
  const { data, isLoading, error, refetch } = useClearingHouseMessage(seq);

  return (
    <Dialog open={seq !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">
            {data ? data.msgDefIdr : "Document"}
          </DialogTitle>
          <DialogDescription>
            {data
              ? `${data.direction === "sent" ? "Sent to" : "Received from"} ${data.counterparty} on ${formatBusinessDate(data.at.slice(0, 10))}${data.orderId ? ` · order ${data.orderId}` : ""}`
              : "The file as it travelled."}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <ErrorState error={error} onRetry={() => void refetch()} />
        ) : isLoading || !data ? (
          <Skeleton className="h-72 w-full" />
        ) : (
          <>
            <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed">
              {data.document}
            </pre>
            <div className="flex items-center justify-between gap-2">
              {/* Rendering a document is not validating it, and this viewer
                  must not imply otherwise: nothing in this repository checks a
                  message against a schema. */}
              <p className="text-xs text-muted-foreground">
                {"The file as it travelled, message id "}
                <span className="font-mono">{data.msgId}</span>
                {". Nothing here checks it against a schema."}
              </p>
              <CopyButton value={data.document} label="document" />
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
