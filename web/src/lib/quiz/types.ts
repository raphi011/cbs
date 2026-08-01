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
  "/",
  // The central bank's home is the reserves table, which is what the questions
  // naming it mean — so this one did not move.
  "/central-bank",
  "/clearing-house",
  "/clearing-house/payments",
  "/clearing-house/mandates",
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
