import { describe, expect, it } from "vitest";

import { accentFor } from "./accent";

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
