import { describe, expect, it } from "vitest";

import { accentFor } from "./accent";

// Two accents can differ in chroma while sharing a hue — bank_7 and the
// clearing house did, at hue 150 — and read as confusingly close at a
// glance even though their oklch() strings aren't literally equal. Pulling
// out the hue is what makes that collision visible to a test.
function hueOf(accent: string | undefined): number {
  const match = accent?.match(/oklch\([^)]+\)/)?.[0];
  const parts = match?.replace(/^oklch\(|\)$/g, "").split(/\s+/) ?? [];
  return Number(parts[2]);
}

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
    const accents = ["bank_1", "bank_3", "bank_5", "bank_7"].map((pid) =>
      accentFor({ persona: "bank", pid }),
    );
    expect(new Set(accents).size).toBe(4);
  });

  // The four seeded banks being distinct from each other, and the two
  // institutions being distinct from each other, says nothing about a bank
  // colliding with an institution — bank_7 and the clearing house used to
  // resolve to the same hue, 150, differing only in chroma. All six seeded
  // accents (two institutions, four banks) must be distinct by hue.
  it("distinguishes all six seeded accents by hue", () => {
    const hues = [
      accentFor({ persona: "central-bank" }),
      accentFor({ persona: "clearing-house" }),
      ...["bank_1", "bank_3", "bank_5", "bank_7"].map((pid) =>
        accentFor({ persona: "bank", pid }),
      ),
    ].map(hueOf);
    expect(new Set(hues).size).toBe(6);
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
