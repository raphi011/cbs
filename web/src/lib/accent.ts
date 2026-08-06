import type { Identity } from "./identity";

// Each identity carries an accent, set as --identity-accent on the shell root. A
// customer inherits their bank's: you are a customer *of* Aurora, and the screen
// should say so without a label.
//
// The two institutions get different accents rather than sharing one, because
// telling them apart at a glance is exactly the lesson the split exists to
// teach.
//
// A bank's hue is picked from a fixed palette by hashing its pid, so its colour
// is stable across reloads and needs nothing persisted. Chroma and lightness are
// held constant so no bank is louder than another and the same values read in
// both themes.
const CENTRAL_BANK_ACCENT = "oklch(0.55 0.02 260)";
const CLEARING_HOUSE_ACCENT = "oklch(0.58 0.10 150)";

// The palette a bank's hue is drawn from, exported so a test can check the whole
// of it rather than a sample of ids.
//
// 150 used to be in this list: a bank hashed onto it and came out at the same hue
// as the clearing house, differing only in chroma — the two read as the same
// colour family at a glance, which is exactly what a bank's accent must not do to
// an institution's. 170 keeps its place without landing on it or on 260.
//
// No hue here may equal an institution's, and that is a property of this LIST
// rather than of any bank: every pid maps into it, so a hue that collides
// collides for whichever banks happen to hash onto it. accent.test.ts checks the
// list itself for that reason. It used to check four seeded ids instead — which
// is a SAMPLE, and a sample of four cannot see a collision that lands on the
// other three hues, whether or not those four ids are current.
export const BANK_HUES = [25, 265, 330, 60, 200, 100, 170];

function hueFor(pid: string): number {
  let h = 0;
  for (let i = 0; i < pid.length; i++) {
    h = (h * 31 + pid.charCodeAt(i)) >>> 0;
  }
  return BANK_HUES[h % BANK_HUES.length];
}

export function accentFor(identity: Identity | null): string | undefined {
  if (!identity) return undefined;
  if (identity.persona === "central-bank") return CENTRAL_BANK_ACCENT;
  if (identity.persona === "clearing-house") return CLEARING_HOUSE_ACCENT;
  return `oklch(0.58 0.14 ${hueFor(identity.pid)})`;
}
