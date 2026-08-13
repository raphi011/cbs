"use client";

import { useState } from "react";
import Link from "next/link";

import { Badge } from "@/components/ui/badge";
import { MessageDialog } from "@/components/message/message-dialog";
import { cn } from "@/lib/utils";
import { codeOf, forReading, isResting } from "@/lib/network-graph";
import { copyToRead, formatSize, holderOf, shortDefinition } from "@/lib/message";
import type { Held, LogHolder } from "@/lib/message";
import { useParticipants } from "@/lib/api/hooks";
import { formatDate } from "@/lib/dates";
import type { Crossing, Institution } from "@/lib/types";

// What crossed, newest first. One row is one FILE and not one movement: a
// delivered file was put by its host and taken by its recipient, and those two
// halves are the ends of a single crossing rather than two events to list.
//
// A row's payments are what the file carried, and each is a link into that
// payment. A file naming none is ordinary — a settlement instruction, a
// statement and a roster all do, and this list is the only place they can be
// read at all.
//
// A row OPENS the document, out of one end's log. Which end is copyToRead's
// rule; a crossing whose sender recorded nothing has only the recipient's copy,
// and one neither end recorded cannot be opened.

// How many of a file's payments are named before the rest are counted. A bulk
// file's whole contents is a document, not a row.
const NAMED = 4;

export function CrossingList({
  crossings,
  institutions,
  dense = false,
  emptyLabel = "Nothing has crossed yet.",
}: {
  crossings: Crossing[];
  // Who plays what part, which is what says whose listener holds a given log.
  institutions: Institution[];
  // The rail is narrow enough that a row has to give up its timestamp.
  dense?: boolean;
  emptyLabel?: string;
}) {
  // A member bank's log is on that bank's own listener, keyed by the id the
  // deployment holds it under rather than by its address.
  const participants = useParticipants();
  const [open, setOpen] = useState<{ holder: LogHolder; seq: number } | null>(null);

  if (crossings.length === 0) {
    return <p className="px-1 py-3 text-sm text-muted-foreground">{emptyLabel}</p>;
  }
  const openable = (held: Held | null): LogHolder | null =>
    held ? holderOf(held.bic, institutions, participants.data ?? []) : null;

  return (
    <>
      <ul className="divide-y">
        {forReading(crossings).map((c) => {
          const held = copyToRead(c);
          const holder = openable(held);
          return (
            <Row
              key={rowKey(c)}
              crossing={c}
              dense={dense}
              onOpen={
                holder && held ? () => setOpen({ holder, seq: held.seq }) : undefined
              }
            />
          );
        })}
      </ul>
      <MessageDialog
        holder={open?.holder ?? null}
        seq={open?.seq ?? null}
        onClose={() => setOpen(null)}
      />
    </>
  );
}

function Row({
  crossing: c,
  dense,
  onOpen,
}: {
  crossing: Crossing;
  dense: boolean;
  // Absent when neither end recorded the file, which is the one row with no
  // document behind it.
  onOpen?: () => void;
}) {
  const unsent = c.sentAt === undefined;
  return (
    <li
      className={cn(
        "-mx-1.5 space-y-1 rounded-md px-1.5 py-2",
        onOpen && "cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground",
      )}
      onClick={onOpen}
      role={onOpen ? "button" : undefined}
      tabIndex={onOpen ? 0 : undefined}
      onKeyDown={(e) => {
        if (onOpen && (e.key === "Enter" || e.key === " ")) {
          e.preventDefault();
          onOpen();
        }
      }}
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span className="truncate font-mono text-xs" title={c.msgDefIdr || c.msgId}>
            {shortDefinition(c.msgDefIdr)}
          </span>
          <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
            {codeOf(c.from)} → {codeOf(c.to)}
          </span>
        </span>
        {isResting(c) ? (
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
          // The row opens the document; a payment link is the other way out of
          // it, and a click must do one or the other.
          onClick={(e) => e.stopPropagation()}
        >
          {shortPayment(id)}
        </Link>
      ))}
      {rest > 0 && <span className="tabular-nums">+{rest}</span>}
    </span>
  );
}

// A payment id is minted per book, so it carries the book's own address and a
// counter — and the address is already on the row, twice. What tells two
// payments in one file apart is the counter, so that is what is shown.
function shortPayment(id: string): string {
  const parts = id.split("_");
  return parts.length > 1 ? parts[parts.length - 1] : id;
}

// A crossing has no id of its own: it is a pair of rows in two institutions'
// logs. Its two ends and the host's order id name it, and where there is no
// order id the sender's message id does.
function rowKey(c: Crossing): string {
  return `${c.from}|${c.to}|${c.orderId || c.msgId}|${c.sentSeq ?? ""}|${c.receivedSeq ?? ""}`;
}
