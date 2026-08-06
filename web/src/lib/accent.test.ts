import { describe, expect, it } from "vitest";

import { accentFor } from "./accent";

// Two accents can differ in chroma while sharing a hue — a seeded bank and the
// clearing house once did, both at hue 150 — and read as confusingly close at a
// glance even though their oklch() strings aren't literally equal. Pulling
// out the hue is what makes that collision visible to a test.
function hueOf(accent: string | undefined): number {
  const match = accent?.match(/oklch\([^)]+\)/)?.[0];
  const parts = match?.replace(/^oklch\(|\)$/g, "").split(/\s+/) ?? [];
  return Number(parts[2]);
}

// The ids the sample dataset's four banks actually have, which is the whole
// point of this fixture: accentFor hashes the id, so a collision test run
// against ids nobody has proves nothing about the screens a reader sees.
//
// They are not consecutive and they move. Every id in this system comes from one
// counter per book, so an act that draws one more than it used to shifts every
// id after it — these four have moved twice while admission became a
// conversation. Read them back from GET /members (the clearing house's console
// lists them) when this test fails on a fresh dataset.
const SEEDED_BANKS = ["bank_1", "bank_9", "bank_17", "bank_25"];

describe("accentFor", () => {
  // Telling the two institutions apart at a glance is exactly the lesson the
  // split exists to teach, so they get different accents rather than sharing one.
  it("gives the two institutions distinct accents", () => {
    const cb = accentFor({ persona: "central-bank" });
    const csm = accentFor({ persona: "clearing-house" });
    expect(cb).toMatch(/^oklch\(/);
    expect(csm).toMatch(/^oklch\(/);
    expect(cb).not.toBe(csm);
  });

  it("is stable for a given bank", () => {
    expect(accentFor({ persona: "bank", pid: "bank_1" })).toBe(
      accentFor({ persona: "bank", pid: "bank_1" }),
    );
  });

  it("distinguishes the four seeded banks", () => {
    const accents = SEEDED_BANKS.map((pid) => accentFor({ persona: "bank", pid }));
    expect(new Set(accents).size).toBe(SEEDED_BANKS.length);
  });

  // The seeded banks being distinct from each other, and the two institutions
  // being distinct from each other, says nothing about a bank colliding with an
  // institution — one seeded bank and the clearing house used to resolve to the
  // same hue, differing only in chroma. Every seeded accent, banks and
  // institutions together, must be distinct by hue.
  it("distinguishes every seeded accent by hue", () => {
    const accents = [
      accentFor({ persona: "central-bank" }),
      accentFor({ persona: "clearing-house" }),
      ...SEEDED_BANKS.map((pid) => accentFor({ persona: "bank", pid })),
    ];
    expect(new Set(accents.map(hueOf)).size).toBe(accents.length);
  });

  // You are a customer *of* Aurora, and the screen should say so without a
  // label — so a customer carries their bank's accent, not one of their own.
  it("gives a customer their bank's accent", () => {
    expect(accentFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toBe(
      accentFor({ persona: "bank", pid: "bank_1" }),
    );
  });

  it("has no accent without an identity", () => {
    expect(accentFor(null)).toBeUndefined();
  });
});
