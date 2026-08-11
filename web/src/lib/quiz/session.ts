import type { Difficulty, Question } from "./types";

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

/**
 * The order a chapter is authored in: five `intro` questions before the first
 * `core` one, and `challenge` last. A question that names no tier sits with
 * `core` rather than at either end, where it would either open the session or
 * close it on the strength of an omission.
 */
const TIER: Record<Difficulty, number> = { intro: 0, core: 1, challenge: 2 };

const tierOf = (q: Question): number => TIER[q.difficulty ?? "core"];

export interface SessionOptions {
  /** Cap the session at this many questions. Omit to use every one. */
  limit?: number;
  /**
   * Concepts the reader has missed before. Up to half the session is drawn
   * from questions on them — enough to make review targeted, not so much that a
   * session becomes only the parts already found hard.
   */
  favour?: ReadonlySet<string>;
}

/** Pick which questions a session asks, before deciding what order to ask them in. */
function selectPool(
  questions: readonly Question[],
  rng: () => number,
  { limit, favour }: SessionOptions,
): Question[] {
  const shuffled = shuffle(questions, rng);
  if (typeof limit !== "number" || limit >= shuffled.length) return shuffled;
  if (!favour?.size) return shuffled.slice(0, limit);

  const weak = shuffled.filter((q) => q.concept != null && favour.has(q.concept));
  const rest = shuffled.filter((q) => !(q.concept != null && favour.has(q.concept)));
  return [...weak.slice(0, Math.ceil(limit / 2)), ...rest].slice(0, limit);
}

export function buildSession(
  questions: readonly Question[],
  seed: number,
  options: SessionOptions = {},
): SessionItem[] {
  const rng = mulberry32(seed);
  const chosen = selectPool(questions, rng, options);

  // Sampling happens before ordering, so a capped session still spans the tiers
  // instead of being the first `limit` intro questions. `sort` is stable, and
  // `chosen` is already shuffled, so this fixes the tier order and leaves the
  // questions within a tier in the random order they arrived in.
  const ordered = [...chosen].sort((a, b) => tierOf(a) - tierOf(b));

  return ordered.map((question) => ({
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

/**
 * A question got wrong, carrying the answer that was given.
 *
 * The response travels with the question because the review screen has to show
 * a reader what they chose, not only what was right — "the correct answer is
 * X" asks them to remember which of four options they clicked a minute ago.
 */
export interface MissedQuestion {
  question: Question;
  response: Response | null;
}

export interface ScoreResult {
  correct: number;
  total: number;
  missed: MissedQuestion[];
}

export function score(
  items: readonly SessionItem[],
  responses: readonly (Response | null)[],
): ScoreResult {
  let correct = 0;
  const missed: MissedQuestion[] = [];
  items.forEach((item, i) => {
    const response = responses[i] ?? null;
    if (isCorrect(item.question, response)) correct++;
    else missed.push({ question: item.question, response });
  });
  return { correct, total: items.length, missed };
}
