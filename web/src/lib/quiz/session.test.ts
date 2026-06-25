import { describe, expect, it } from "vitest";

import {
  mulberry32,
  shuffle,
  buildSession,
  isCorrect,
  score,
  type Response,
} from "./session";
import type { Question } from "./types";

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
  unit: "dollars",
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
    expect(buildSession([mc, tf, multi, num], 1, 2)).toHaveLength(2);
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
    expect(r.missed.map((q) => q.id)).toEqual(["t-tf"]);
  });
});
