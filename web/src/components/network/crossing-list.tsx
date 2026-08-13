"use client";

import Link from "next/link";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { codeOf, forReading } from "@/lib/network-graph";
import { formatDate } from "@/lib/dates";
import type { Crossing } from "@/lib/types";

// What crossed, newest first. One row is one FILE and not one movement: a
// delivered file was put by its host and taken by its recipient, and those two
// halves are the ends of a single crossing rather than two events to list.
//
// A row's payments are what the file carried, and each is a link into that
// payment. A file naming none is ordinary — a settlement instruction and a
// roster both do.

// How many of a file's payments are named before the rest are counted. A bulk
// file's whole contents is a document, not a row.
const NAMED = 4;

export function CrossingList({
  crossings,
  dense = false,
  emptyLabel = "Nothing has crossed yet.",
}: {
  crossings: Crossing[];
  // The rail is narrow enough that a row has to give up its timestamp.
  dense?: boolean;
  emptyLabel?: string;
}) {
  if (crossings.length === 0) {
    return <p className="px-1 py-3 text-sm text-muted-foreground">{emptyLabel}</p>;
  }
  return (
    <ul className="divide-y">
      {forReading(crossings).map((c) => (
        <Row key={rowKey(c)} crossing={c} dense={dense} />
      ))}
    </ul>
  );
}

function Row({ crossing: c, dense }: { crossing: Crossing; dense: boolean }) {
  const resting = c.sentAt !== undefined && c.receivedAt === undefined;
  const unsent = c.sentAt === undefined;
  return (
    <li className="space-y-1 py-2">
      <div className="flex items-baseline justify-between gap-2">
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span className="truncate font-mono text-xs" title={c.msgDefIdr || c.msgId}>
            {shortDefinition(c.msgDefIdr)}
          </span>
          <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
            {codeOf(c.from)} → {codeOf(c.to)}
          </span>
        </span>
        {resting ? (
          // Not an error and not a failure: a file put where its recipient can
          // reach it, which that recipient collects in its own phase.
          <Badge variant="outline" className="shrink-0 border-primary/40 text-primary">
            waiting
          </Badge>
        ) : unsent ? (
          // The sender holds no record of a file its counterparty received,
          // which is a missing record rather than a state a file passes through.
          <Badge variant="destructive" className="shrink-0">
            no send recorded
          </Badge>
        ) : (
          // The DAY and not the time. A deployment's clock does not move within
          // a day, so every file that crossed on one carries the same instant —
          // rendering it would claim a precision the timeline does not have.
          !dense && (
            <span className="shrink-0 text-xs text-muted-foreground">
              {formatDate(c.receivedAt)}
            </span>
          )
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span className="tabular-nums">{formatSize(c.payloadSize)}</span>
        {c.payments.length > 0 && <Payments ids={c.payments} />}
      </div>
    </li>
  );
}

// The payments a file carried, each a link to what became of it. The clearing
// house's page is where they go because it is the one console that holds every
// payment in the network — a bank sees only its own legs.
function Payments({ ids }: { ids: string[] }) {
  const named = ids.slice(0, NAMED);
  const rest = ids.length - named.length;
  return (
    <span className="flex flex-wrap items-center gap-1">
      {named.map((id) => (
        <Link
          key={id}
          href={`/clearing-house/payments/${id}`}
          className={cn(
            "rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground",
            "transition-colors hover:bg-accent hover:text-accent-foreground",
          )}
          title={id}
        >
          {shortPayment(id)}
        </Link>
      ))}
      {rest > 0 && <span className="tabular-nums">+{rest}</span>}
    </span>
  );
}

// A message definition identifier is a family, a variant and a version —
// "pacs.008.001.10". The family and the variant are what names the document;
// the version is what an implementer needs and a reader does not.
function shortDefinition(msgDefIdr: string): string {
  if (!msgDefIdr) return "file";
  const parts = msgDefIdr.split(".");
  return parts.length >= 2 ? `${parts[0]}.${parts[1]}` : msgDefIdr;
}

// A payment id is minted per book, so it carries the book's own address and a
// counter — and the address is already on the row, twice. What tells two
// payments in one file apart is the counter, so that is what is shown.
function shortPayment(id: string): string {
  const parts = id.split("_");
  return parts.length > 1 ? parts[parts.length - 1] : id;
}

function formatSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} kB`;
}

// A crossing has no id of its own: it is a pair of rows in two institutions'
// logs. Its two ends and the host's order id name it, and where there is no
// order id the sender's message id does.
function rowKey(c: Crossing): string {
  return `${c.from}|${c.to}|${c.orderId || c.msgId}|${c.sentSeq ?? ""}|${c.receivedSeq ?? ""}`;
}
