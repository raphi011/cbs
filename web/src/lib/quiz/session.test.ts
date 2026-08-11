import { describe, expect, it } from "vitest";

import {
  mulberry32,
  shuffle,
  buildSession,
  isCorrect,
  score,
  type Response,
} from "./session";
import type { Difficulty, Question } from "./types";
import type { HintKey } from "@/components/hint-content";

const mc: Question = {
  kind: "mc",
  id: "t-mc",
  prompt: "p",
  explanation: "e",
  options: ["a", "b", "c", "d"],
  answer: 2,
};
const tf: Question = { kind: "truefalse", id: "t-tf", prompt: "p", explanation: "e", answer: true };
const multi: Question = {
  kind: "multi",
  id: "t-multi",
  prompt: "p",
  explanation: "e",
  options: ["a", "b", "c"],
  answers: [0, 2],
};
const num: Question = {
  kind: "numeric",
  id: "t-num",
  prompt: "p",
  explanation: "e",
  answer: 100,
  unit: { asset: "USD", in: "major" },
  tolerance: 1,
};

describe("mulberry32", () => {
  it("is deterministic for a seed", () => {
    const a = mulberry32(42);
    const b = mulberry32(42);
    expect([a(), a(), a()]).toEqual([b(), b(), b()]);
  });
});

describe("shuffle", () => {
  it("returns a permutation (same multiset)", () => {
    const out = shuffle([1, 2, 3, 4, 5], mulberry32(7));
    expect([...out].sort((x, y) => x - y)).toEqual([1, 2, 3, 4, 5]);
  });
});

describe("buildSession", () => {
  it("is deterministic for the same seed", () => {
    const a = buildSession([mc, tf, multi, num], 123);
    const b = buildSession([mc, tf, multi, num], 123);
    expect(a.map((i) => i.question.id)).toEqual(b.map((i) => i.question.id));
    expect(a[0].optionOrder).toEqual(b[0].optionOrder);
  });

  it("gives mc/multi an option permutation and others an empty order", () => {
    const items = buildSession([mc, tf, multi, num], 9);
    const mcItem = items.find((i) => i.question.id === "t-mc")!;
    expect([...mcItem.optionOrder].sort((x, y) => x - y)).toEqual([0, 1, 2, 3]);
    expect(items.find((i) => i.question.id === "t-tf")!.optionOrder).toEqual([]);
    expect(items.find((i) => i.question.id === "t-num")!.optionOrder).toEqual([]);
  });

  it("respects the limit", () => {
    expect(buildSession([mc, tf, multi, num], 1, { limit: 2 })).toHaveLength(2);
  });

  it("asks intro before core before challenge", () => {
    const tier = (id: string, difficulty: Difficulty): Question => ({
      ...mc,
      id,
      difficulty,
    });
    const pool = [
      tier("c1", "challenge"),
      tier("i1", "intro"),
      tier("k1", "core"),
      tier("c2", "challenge"),
      tier("i2", "intro"),
      tier("k2", "core"),
    ];
    // Every seed, because the ramp must not depend on which shuffle came out.
    for (const seed of [1, 2, 3, 42, 1000]) {
      const got = buildSession(pool, seed).map((i) => i.question.id[0]);
      expect(got).toEqual(["i", "i", "k", "k", "c", "c"]);
    }
  });

  it("treats a question with no difficulty as core", () => {
    const plain: Question = { ...mc, id: "plain" };
    const intro: Question = { ...mc, id: "intro", difficulty: "intro" };
    const hard: Question = { ...mc, id: "hard", difficulty: "challenge" };
    const got = buildSession([hard, plain, intro], 7).map((i) => i.question.id);
    expect(got).toEqual(["intro", "plain", "hard"]);
  });

  it("samples before ordering, so a capped session still spans the tiers", () => {
    const many: Question[] = [
      ...Array.from({ length: 10 }, (_, i) => ({ ...mc, id: `i${i}`, difficulty: "intro" as const })),
      ...Array.from({ length: 10 }, (_, i) => ({ ...mc, id: `c${i}`, difficulty: "challenge" as const })),
    ];
    const tiers = new Set(buildSession(many, 3, { limit: 10 }).map((i) => i.question.difficulty));
    expect(tiers).toEqual(new Set(["intro", "challenge"]));
  });

  it("draws favoured concepts first, up to half the session", () => {
    const on = (id: string, concept: HintKey): Question => ({ ...mc, id, concept });
    const pool = [
      ...Array.from({ length: 8 }, (_, i) => on(`weak${i}`, "normal-balance")),
      ...Array.from({ length: 8 }, (_, i) => on(`other${i}`, "value-date")),
    ];
    const ids = buildSession(pool, 11, {
      limit: 4,
      favour: new Set(["normal-balance"]),
    }).map((i) => i.question.id);

    expect(ids.filter((id) => id.startsWith("weak"))).toHaveLength(2);
    expect(ids.filter((id) => id.startsWith("other"))).toHaveLength(2);
  });

  it("ignores the favour set when nothing is capped", () => {
    const pool = [mc, tf, multi, num];
    const all = buildSession(pool, 5, { favour: new Set(["normal-balance"]) });
    expect(all).toHaveLength(4);
  });
});

describe("isCorrect", () => {
  it("grades mc by index", () => {
    expect(isCorrect(mc, { kind: "mc", choice: 2 })).toBe(true);
    expect(isCorrect(mc, { kind: "mc", choice: 0 })).toBe(false);
  });
  it("grades truefalse", () => {
    expect(isCorrect(tf, { kind: "truefalse", choice: true })).toBe(true);
    expect(isCorrect(tf, { kind: "truefalse", choice: false })).toBe(false);
  });
  it("grades multi independent of order", () => {
    expect(isCorrect(multi, { kind: "multi", choices: [2, 0] })).toBe(true);
    expect(isCorrect(multi, { kind: "multi", choices: [0] })).toBe(false);
    expect(isCorrect(multi, { kind: "multi", choices: [0, 1, 2] })).toBe(false);
  });
  it("grades numeric within tolerance", () => {
    expect(isCorrect(num, { kind: "numeric", value: 100.5 })).toBe(true);
    expect(isCorrect(num, { kind: "numeric", value: 102 })).toBe(false);
  });
  it("is false for null or mismatched-kind responses", () => {
    expect(isCorrect(mc, null)).toBe(false);
    expect(isCorrect(mc, { kind: "numeric", value: 2 } as Response)).toBe(false);
  });
});

describe("score", () => {
  it("counts correct and collects missed", () => {
    const items = buildSession([mc, tf], 5);
    const responses: (Response | null)[] = items.map((i) =>
      i.question.id === "t-mc" ? { kind: "mc", choice: 2 } : null,
    );
    const r = score(items, responses);
    expect(r.correct).toBe(1);
    expect(r.total).toBe(2);
    expect(r.missed.map((m) => m.question.id)).toEqual(["t-tf"]);
  });

  it("carries the answer that was given, so a review can show it back", () => {
    const items = buildSession([mc], 5);
    const given: Response = { kind: "mc", choice: 0 };
    expect(score(items, [given]).missed[0].response).toEqual(given);
  });

  it("reports an unanswered question as missed with a null response", () => {
    const items = buildSession([mc], 5);
    expect(score(items, [null]).missed[0].response).toBeNull();
  });
});
