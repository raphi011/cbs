import type { HintKey } from "@/components/hint-content";
import type { NumericUnit } from "./units";

export type QuestionKind = "mc" | "truefalse" | "multi" | "numeric";
export type Difficulty = "intro" | "core" | "challenge";

/**
 * Explorer routes a question may deep-link to.
 *
 * It lives here rather than in index.ts so `explore.href` can be typed against
 * it: index.ts imports the chapters and the chapters import this file, so the
 * other direction would be a cycle. Typing it is what makes the compiler hold
 * every chapter to the allowlist — before this it was `string`, and only a
 * runtime test caught a stale one.
 */
export const EXPLORE_ROUTES = [
  // The lobby. "See the ledger" still lands somewhere real: the cast, and a
  // seat to pick.
  //
  // It is also where the three MANDATE questions in chapter 12 point, and that
  // is a decision rather than a fallback. Mandates moved to a BANK's console at
  // Task 18b — /bank/{pid}/mandates — because a mandate is the creditor's bank's
  // row and no listing spans them, and a route carrying a pid is one this list
  // cannot hold: it would name a seeded bank, so the curriculum would depend on
  // the fixture and a renamed bank would break a question. Leaving the three
  // questions with no link at all was the other option and is worse — the point
  // of a deep link is to send a reader to the thing, and "pick a bank, then
  // Mandates" is one extra click that also happens to TEACH the fact the
  // question is about: there is no network-wide mandate list to link to,
  // because a mandate belongs to one bank.
  "/",
  // The central bank's home is the reserves table, which is what the questions
  // naming it mean — so this one did not move.
  "/central-bank",
  "/clearing-house",
  "/clearing-house/payments",
  "/clearing-house/cycles",
  "/clearing-house/settlements",
  "/clearing-house/schemes",
] as const;

export type ExploreRoute = (typeof EXPLORE_ROUTES)[number];

interface BaseQuestion {
  /** Stable, globally unique, e.g. "ch2-q3". */
  id: string;
  /** The question text (plain text). */
  prompt: string;
  /** Shown after answering. Supports markdown and [[concept]] wiki-links. */
  explanation: string;
  /** Drives the right sidebar while this question is on screen. */
  concept?: HintKey;
  /** Optional deep-link to a relevant explorer page (operator-level routes only). */
  explore?: { href: ExploreRoute; label: string };
  difficulty?: Difficulty;
}

export type Question =
  | (BaseQuestion & { kind: "mc"; options: string[]; answer: number })
  | (BaseQuestion & { kind: "truefalse"; answer: boolean })
  | (BaseQuestion & { kind: "multi"; options: string[]; answers: number[] })
  | (BaseQuestion & {
      kind: "numeric";
      answer: number;
      /**
       * What the answer counts. Omit for a bare number. The old shape was
       * `"cents" | "dollars"`, which made an answer in satoshi unwritable —
       * there was no correct label for one and no way to add it.
       */
      unit?: NumericUnit;
      /** Accepted absolute deviation from `answer`; defaults to 0. */
      tolerance?: number;
    });

export interface Chapter {
  /** Matches the book filename stem, e.g. "02-double-entry-bookkeeping". */
  slug: string;
  /** 1..18 */
  number: number;
  /** Book Part heading, used to group chapters on the index. */
  part: string;
  title: string;
  questions: Question[];
}
