// How a file is named and sized on screen, and which log a document is read
// from. The rules are here rather than in a component because none of them is
// React and because there is no component runner in this repo.

import type { Crossing, Institution, MessageDirection, Participant } from "./types";

// A message definition identifier is a family, a variant and a version —
// "pacs.008.001.10". The family and the variant are what names the document;
// the version is what an implementer needs and a reader does not.
export function shortDefinition(msgDefIdr: string): string {
  if (!msgDefIdr) return "file";
  const parts = msgDefIdr.split(".");
  return parts.length >= 2 ? `${parts[0]}.${parts[1]}` : msgDefIdr;
}

// A file's size, which is what a listing carries in place of the file. Bytes up
// to a kilobyte, because a document small enough to count in bytes is one worth
// knowing is nearly empty.
export function formatSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} kB`;
}

// A LogHolder is which listener answers for one institution's log. Every
// institution keeps its own and each listener answers about itself alone, so
// reaching a document means naming the institution as well as the seq.
export type LogHolder =
  | { kind: "bank"; pid: string }
  | { kind: "clearing house" }
  | { kind: "settlement agent" };

// holderOf resolves an address to the listener holding its log, out of the
// mesh's own roll of who plays what part. A member bank is looked UP rather
// than assumed to be keyed by its own address; an address the mesh does not
// list, or a bank the deployment holds no row for, is nobody's.
export function holderOf(
  bic: string,
  institutions: Institution[],
  participants: Participant[],
): LogHolder | null {
  switch (institutions.find((i) => i.bic === bic)?.role) {
    case "clearing house":
      return { kind: "clearing house" };
    case "settlement agent":
      return { kind: "settlement agent" };
    case "member bank": {
      const p = participants.find((p) => p.bic === bic);
      return p ? { kind: "bank", pid: p.id } : null;
    }
    default:
      return null;
  }
}

// holderKey names a holder for a cache, and it is the operator key: what tells
// two listeners apart is the whole of what a seq needs qualifying by.
export function holderKey(h: LogHolder): string {
  return h.kind === "bank" ? `bank/${h.pid}` : h.kind;
}

// holderName is whose log a reader is looking at. The two hosts are named by
// the part they play, because a deployment has one of each and their addresses
// say less than their roles do.
export function holderName(h: LogHolder): string {
  return h.kind === "bank" ? h.pid : `the ${h.kind}`;
}

// Held is one end's copy of a crossing: whose log it is in, and the seq that
// names it there.
export interface Held {
  bic: string;
  seq: number;
  direction: MessageDirection;
}

// copyToRead is which of a crossing's two rows a reader opens.
//
// The SENDER's, because that is the file as it was sent and it exists for every
// crossing that happened. A crossing with no send recorded is a missing record
// rather than a stage a file passes through, and the recipient's copy is then
// the only one there is. Both are the same bytes; which log they came out of is
// what the viewer names.
export function copyToRead(c: Crossing): Held | null {
  if (c.sentSeq !== undefined) return { bic: c.from, seq: c.sentSeq, direction: "sent" };
  if (c.receivedSeq !== undefined) {
    return { bic: c.to, seq: c.receivedSeq, direction: "received" };
  }
  return null;
}
