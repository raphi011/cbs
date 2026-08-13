import { describe, expect, it } from "vitest";

import { trailOf, waitingOn } from "./payment-trail";
import { hintContent } from "@/components/hint-content";
import type { AuditEvent } from "./types";

const PAY = "pay_NORDSESSXXX_721";

// Every event of one business day carries that day's instant, so a trail that
// sorted by time would be sorting four equal values.
const DAY = "2026-05-18T09:00:00Z";

function event(seq: number, type: string, timestamp = DAY): AuditEvent {
  return {
    seq,
    id: `evt_${seq}`,
    scope: "payment",
    timestamp,
    type,
    entityId: PAY,
  };
}

describe("trailOf", () => {
  it("orders by seq and not by the timestamps, which all tie", () => {
    const steps = trailOf([
      event(60, "payment.settled"),
      event(49, "payment.initiated"),
      event(55, "payment.cleared"),
      event(50, "payment.accepted"),
    ]);
    expect(steps.map((s) => s.title)).toEqual([
      "Initiated",
      "Accepted",
      "Cleared",
      "Settled",
    ]);
  });

  it("leaves an older seq first even when a later day carries a lower one", () => {
    // Two seq counters never meet, but one institution's does not restart: a
    // step recorded on a later day has the higher seq, and the ordering is the
    // seq's either way.
    const steps = trailOf([
      event(80, "payment.settled", "2026-05-19T09:00:00Z"),
      event(49, "payment.initiated"),
    ]);
    expect(steps.map((s) => s.seq)).toEqual([49, 80]);
    expect(steps.map((s) => s.day)).toEqual(["2026-05-18", "2026-05-19"]);
  });

  it("keeps an event it does not recognise, under its own type", () => {
    const steps = trailOf([event(12, "payment.something_new")]);
    expect(steps).toHaveLength(1);
    expect(steps[0].title).toBe("payment.something_new");
    expect(steps[0].body).toBeUndefined();
  });

  it("names only hints that exist, which nothing else checks for a step", () => {
    const types = [
      "payment.initiated",
      "payment.accepted",
      "payment.cleared",
      "payment.settled",
      "payment.rejected",
      "payment.returned",
    ];
    for (const s of trailOf(types.map((t, i) => event(i + 1, t)))) {
      expect(s.body).toBeTruthy();
      expect(hintContent[s.hint!]).toBeDefined();
    }
  });

  it("does not mutate what it was handed", () => {
    const events = [event(60, "payment.settled"), event(49, "payment.initiated")];
    trailOf(events);
    expect(events.map((e) => e.seq)).toEqual([60, 49]);
  });
});

describe("waitingOn", () => {
  it("names the operator's act for a payment that has stopped", () => {
    for (const status of ["Initiated", "Accepted", "Cleared"] as const) {
      expect(waitingOn(status)?.act).toBe("Advance day");
    }
  });

  it("says nothing about a payment that is finished", () => {
    for (const status of ["Settled", "Rejected", "Returned"] as const) {
      expect(waitingOn(status)).toBeUndefined();
    }
  });
});
