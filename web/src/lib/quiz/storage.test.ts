import { describe, expect, it } from "vitest";

import {
  clearSession,
  readMissCounts,
  readMissedConcepts,
  readProgress,
  readSession,
  recordConceptOutcomes,
  recordResult,
  writeSession,
  type SavedSession,
} from "./storage";

function fakeStore(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    clear: () => m.clear(),
    getItem: (k: string) => (m.has(k) ? (m.get(k) as string) : null),
    key: (i: number) => [...m.keys()][i] ?? null,
    removeItem: (k: string) => {
      m.delete(k);
    },
    setItem: (k: string, v: string) => {
      m.set(k, String(v));
    },
  };
}

describe("progress storage", () => {
  it("returns null when nothing is stored", () => {
    expect(readProgress("02-double-entry-bookkeeping", fakeStore())).toBeNull();
  });

  it("records and reads back a result", () => {
    const s = fakeStore();
    recordResult("02-double-entry-bookkeeping", 80, "2026-06-25T00:00:00.000Z", s);
    expect(readProgress("02-double-entry-bookkeeping", s)).toEqual({
      bestPct: 80,
      lastPct: 80,
      lastAttempt: "2026-06-25T00:00:00.000Z",
    });
  });

  it("keeps the maximum as bestPct but updates lastPct", () => {
    const s = fakeStore();
    recordResult("ch", 80, "t1", s);
    const after = recordResult("ch", 60, "t2", s);
    expect(after.bestPct).toBe(80);
    expect(after.lastPct).toBe(60);
    expect(after.lastAttempt).toBe("t2");
  });

  it("returns null on corrupt JSON", () => {
    const s = fakeStore();
    s.setItem("quiz:ch", "{not json");
    expect(readProgress("ch", s)).toBeNull();
  });

  it("drops a corrupt entry rather than leaving it to fail on every visit", () => {
    const s = fakeStore();
    s.setItem("quiz:ch", "{not json");
    readProgress("ch", s);
    expect(s.getItem("quiz:ch")).toBeNull();
  });

  it("does not throw when no storage is available", () => {
    expect(() => recordResult("ch", 50, "t")).not.toThrow();
    expect(readProgress("ch")).toBeNull();
  });
});

describe("session storage", () => {
  const session: SavedSession = {
    seed: 99,
    index: 3,
    ids: ["a", "b", "c", "d"],
    responses: [{ kind: "mc", choice: 1 }, null, null, null],
    streak: 2,
  };

  it("round-trips a session in flight", () => {
    const s = fakeStore();
    writeSession("ch", session, s);
    expect(readSession("ch", s)).toEqual(session);
  });

  it("has nothing to resume before one is written", () => {
    expect(readSession("ch", fakeStore())).toBeNull();
  });

  it("clears a session once it is finished", () => {
    const s = fakeStore();
    writeSession("ch", session, s);
    clearSession("ch", s);
    expect(readSession("ch", s)).toBeNull();
  });

  it("keeps a chapter's session separate from the mixed one", () => {
    const s = fakeStore();
    writeSession("ch", session, s);
    writeSession("mixed", { ...session, seed: 1, ids: ["z"] }, s);
    expect(readSession("ch", s)?.seed).toBe(99);
    expect(readSession("mixed", s)?.ids).toEqual(["z"]);
  });

  it("refuses a session whose shape it cannot trust", () => {
    const s = fakeStore();
    s.setItem("quiz:session:ch", JSON.stringify({ seed: 1, index: 0 }));
    expect(readSession("ch", s)).toBeNull();
    expect(s.getItem("quiz:session:ch")).toBeNull();
  });

  it("refuses ids that are not all strings", () => {
    const s = fakeStore();
    s.setItem(
      "quiz:session:ch",
      JSON.stringify({ ...session, ids: ["a", 2] }),
    );
    expect(readSession("ch", s)).toBeNull();
  });
});

describe("concept miss tracking", () => {
  it("starts empty", () => {
    expect(readMissedConcepts(fakeStore()).size).toBe(0);
  });

  it("records a miss and reports the concept", () => {
    const s = fakeStore();
    recordConceptOutcomes(["value-date"], [], s);
    expect([...readMissedConcepts(s)]).toEqual(["value-date"]);
  });

  it("counts repeated misses of one concept", () => {
    const s = fakeStore();
    recordConceptOutcomes(["value-date"], [], s);
    recordConceptOutcomes(["value-date"], [], s);
    expect(readMissCounts(s)["value-date"]).toBe(2);
  });

  it("forgives one miss per correct answer, and forgets at zero", () => {
    const s = fakeStore();
    recordConceptOutcomes(["value-date", "value-date"], [], s);
    recordConceptOutcomes([], ["value-date"], s);
    expect(readMissCounts(s)["value-date"]).toBe(1);

    recordConceptOutcomes([], ["value-date"], s);
    expect(readMissedConcepts(s).has("value-date")).toBe(false);
    expect("value-date" in readMissCounts(s)).toBe(false);
  });

  it("does not go negative on a concept that was never missed", () => {
    const s = fakeStore();
    recordConceptOutcomes([], ["normal-balance"], s);
    expect(readMissedConcepts(s).size).toBe(0);
  });

  it("drops a corrupt miss record", () => {
    const s = fakeStore();
    s.setItem("quiz:missed", "[1,2,3]");
    expect(readMissCounts(s)).toEqual({});
    expect(s.getItem("quiz:missed")).toBeNull();
  });
});
