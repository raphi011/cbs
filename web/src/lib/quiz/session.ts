import type { Question } from "./types";

/** A reader's answer; its shape depends on the question kind. */
export type Response =
  | { kind: "mc"; choice: number }
  | { kind: "truefalse"; choice: boolean }
  | { kind: "multi"; choices: number[] }
  | { kind: "numeric"; value: number };

/** A question prepared for display, with its options pre-shuffled. */
export interface SessionItem {
  question: Question;
  /** Display order of option indices for mc/multi; [] for truefalse/numeric. */
  optionOrder: number[];
}

/** Small deterministic PRNG so (questions, seed) always yields the same session. */
export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function shuffle<T>(input: readonly T[], rng: () => number): T[] {
  const arr = [...input];
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(rng() * (i + 1));
    [arr[i], arr[j]] = [arr[j], arr[i]];
  }
  return arr;
}

function optionCount(q: Question): number {
  return q.kind === "mc" || q.kind === "multi" ? q.options.length : 0;
}

export function buildSession(
  questions: readonly Question[],
  seed: number,
  limit?: number,
): SessionItem[] {
  const rng = mulberry32(seed);
  const ordered = shuffle(questions, rng);
  const chosen = typeof limit === "number" ? ordered.slice(0, limit) : ordered;
  return chosen.map((question) => ({
    question,
    optionOrder: shuffle(
      Array.from({ length: optionCount(question) }, (_, i) => i),
      rng,
    ),
  }));
}

export function isCorrect(q: Question, r: Response | null): boolean {
  if (!r || r.kind !== q.kind) return false;
  switch (q.kind) {
    case "mc":
      return r.kind === "mc" && r.choice === q.answer;
    case "truefalse":
      return r.kind === "truefalse" && r.choice === q.answer;
    case "multi": {
      if (r.kind !== "multi") return false;
      const want = [...q.answers].sort((x, y) => x - y);
      const got = [...r.choices].sort((x, y) => x - y);
      return want.length === got.length && want.every((v, i) => v === got[i]);
    }
    case "numeric":
      return r.kind === "numeric" && Math.abs(r.value - q.answer) <= (q.tolerance ?? 0);
  }
}

export interface ScoreResult {
  correct: number;
  total: number;
  missed: Question[];
}

export function score(
  items: readonly SessionItem[],
  responses: readonly (Response | null)[],
): ScoreResult {
  let correct = 0;
  const missed: Question[] = [];
  items.forEach((item, i) => {
    if (isCorrect(item.question, responses[i] ?? null)) correct++;
    else missed.push(item.question);
  });
  return { correct, total: items.length, missed };
}
