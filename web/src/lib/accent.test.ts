import { describe, expect, it } from "vitest";

import { accentFor, BANK_HUES } from "./accent";

// Two accents can differ in chroma while sharing a hue — a seeded bank and the
// clearing house once did, both at hue 150 — and read as confusingly close at a
// glance even though their oklch() strings aren't literally equal. Pulling
// out the hue is what makes that collision visible to a test.
function hueOf(accent: string | undefined): number {
  const match = accent?.match(/oklch\([^)]+\)/)?.[0];
  const parts = match?.replace(/^oklch\(|\)$/g, "").split(/\s+/) ?? [];
  return Number(parts[2]);
}

// Every id the backend's counter could plausibly have reached, rather than the
// four the seed produces.
//
// The list this replaces was CORRECT when it was deleted — `bank_1`, `bank_9`,
// `bank_17`, `bank_25` are the seed's ids today. Two things were wrong with it
// anyway, and being current was not one of them.
//
// It goes stale silently. Every id in the backend comes from one counter per
// book, so an act that draws one more than it used to shifts every id after it —
// which happened during this very task, leaving this file asserting about
// `bank_1/3/5/7` while the seed produced something else. Nothing failed. A list
// of ids nobody has still passes, and it passes while real banks collide.
//
// And even current it is a SAMPLE. Four ids reach at most four of the palette's
// hues, so a hue that collides with an institution goes unseen unless one of
// those four happens to land on it — which is the collision this file exists to
// catch, missed by the instrument written to catch it.
//
// A vitest suite cannot read the Go seed, so it stops naming ids. Forty reaches
// the whole palette, and the sweep checks that it still does rather than quietly
// sampling part of it.
const SWEPT_BANKS = Array.from({ length: 40 }, (_, i) => `bank_${i + 1}`);

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

  // Banks are spread across the palette rather than piling onto part of it. It
  // is the strongest form the old "the four seeded banks are distinct" claim can
  // take: a hash into a fixed palette CANNOT promise two given banks differ —
  // with more banks than hues, two of them must share — so what is checkable is
  // that every hue is in use and none of the network is invisible.
  //
  // It is also what keeps the sweep below honest. If the palette grew past what
  // forty ids reach, this fails and names the gap instead of leaving a hue
  // unchecked.
  it("spreads banks across the whole palette", () => {
    const hues = SWEPT_BANKS.map((pid) => hueOf(accentFor({ persona: "bank", pid })));
    expect(new Set(hues)).toEqual(new Set(BANK_HUES));
  });

  // No bank may share an institution's hue, which is the collision this file was
  // written for: a bank once landed on 150 and read as the clearing house,
  // differing only in chroma.
  //
  // Checked over the PALETTE and not over a set of banks, because that is the
  // whole of the property — every pid maps into that list, so a hue that
  // collides collides for whichever banks hash onto it, and a list with no
  // collision in it cannot produce one for any id that will ever exist. A test
  // over ids is a sample of this one.
  it("keeps every hue a bank can take off both institutions", () => {
    const institutions = [
      hueOf(accentFor({ persona: "central-bank" })),
      hueOf(accentFor({ persona: "clearing-house" })),
    ];
    for (const hue of BANK_HUES) {
      expect(institutions).not.toContain(hue);
    }
  });

  // And the mapping really does draw from that list, so the check above is a
  // check on what banks actually get.
  it("gives every bank a hue from the palette", () => {
    for (const pid of SWEPT_BANKS) {
      expect(BANK_HUES).toContain(hueOf(accentFor({ persona: "bank", pid })));
    }
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
