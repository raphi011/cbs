// Where a payment has been, read off ONE institution's audit log.
//
// It is here rather than inside the drawing of it because none of it is React,
// and because there is no component runner in this repo — the ordering rule and
// the sentence a stopped payment gets are only pinned by a test if they can be
// reached without rendering.

import type { HintKey } from "@/components/hint-content";
import type { PaymentStatus } from "./enums";
import type { AuditEvent } from "./types";

// A step is one thing that happened to this institution's copy. `body` says
// what the payment's state means and never which institution wrote the row: the
// same trail renders a bank's audit unchanged, and the two would legitimately
// disagree about the status.
export interface TrailStep {
  seq: number;
  type: string;
  title: string;
  body?: string;
  hint?: HintKey;
  // The DAY, because a deployment's clock does not move within one and
  // rendering a time would claim a precision this timeline does not have.
  day: string;
}

const STEPS: Record<string, { title: string; body: string; hint: HintKey }> = {
  "payment.initiated": {
    title: "Initiated",
    body: "The instruction exists and nothing has been sent. It waits in the submitting bank's hub until that bank's next cut-off.",
    hint: "payment-hub",
  },
  "payment.accepted": {
    title: "Accepted",
    body: "A bulk file carried it to the clearing house, which took it into the open cycle for its scheme.",
    hint: "bulk-file",
  },
  "payment.cleared": {
    title: "Cleared",
    body: "Its cycle closed and netted. Positions are computed and no money has moved.",
    hint: "clearing-vs-settlement",
  },
  "payment.settled": {
    title: "Settled",
    body: "Reserves moved at the settlement agent, and only then was the file released to the payee's bank.",
    hint: "settlement-finality",
  },
  "payment.rejected": {
    title: "Rejected",
    body: "Declined before settlement, and the submitting bank reverses the debtor leg it had already posted.",
    hint: "payment-lifecycle",
  },
  "payment.returned": {
    title: "Returned",
    body: "Unwound after settlement, which is a second flow in the other direction rather than an undo.",
    hint: "allows-return",
  },
};

// trailOf is one institution's events for one payment, in the order they
// happened.
//
// It orders by SEQ and not by timestamp, which is the constraint rather than a
// preference: every event of one business day carries that day's instant, so
// four steps of a day are four identical timestamps. A seq is a total order
// within one database and meaningless between two, which is why nothing here
// merges two institutions' answers.
//
// An event this table does not name still becomes a step, under its own type. A
// trail that dropped what it did not recognise would be a trail with a hole in
// it.
export function trailOf(events: AuditEvent[]): TrailStep[] {
  return [...events]
    .sort((a, b) => a.seq - b.seq)
    .map((e) => ({
      seq: e.seq,
      type: e.type,
      title: STEPS[e.type]?.title ?? e.type,
      body: STEPS[e.type]?.body,
      hint: STEPS[e.type]?.hint,
      day: e.timestamp.slice(0, 10),
    }));
}

// What is waiting on what, for a payment that has stopped somewhere short of an
// end. `act` is named in the words of the button that runs it, and it is the
// OPERATOR's act: nothing a participant does moves a payment out of any of
// these three states.
export interface Waiting {
  because: string;
  act: string;
}

const WAITING: Partial<Record<PaymentStatus, Waiting>> = {
  Initiated: {
    because: "Waiting here. The submitting bank's next cut-off is what sends it.",
    act: "Advance day",
  },
  Accepted: {
    because: "Waiting here. Its cycle closes at the clearing house's next cut-off.",
    act: "Advance day",
  },
  Cleared: {
    because: "Waiting here. The settlement agent discharges the net positions.",
    act: "Advance day",
  },
};

// waitingOn answers nothing for a payment that is finished — settled, rejected
// or returned — because a finished payment is not waiting on anybody.
export function waitingOn(status: PaymentStatus): Waiting | undefined {
  return WAITING[status];
}
