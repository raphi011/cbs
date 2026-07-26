import type { Chapter, Question } from "./types";

import { chapter as ch01 } from "./chapters/01-what-a-bank-is";
import { chapter as ch02 } from "./chapters/02-double-entry-bookkeeping";
import { chapter as ch03 } from "./chapters/03-the-chart-of-accounts";
import { chapter as ch04 } from "./chapters/04-ledgers-subledgers-and-money";
import { chapter as ch05 } from "./chapters/05-transactions-and-postings";
import { chapter as ch06 } from "./chapters/06-booking-date-vs-value-date";
import { chapter as ch07 } from "./chapters/07-balances-and-holds";
import { chapter as ch08 } from "./chapters/08-account-lifecycle-and-overdraft";
import { chapter as ch09 } from "./chapters/09-clearing-and-settlement";
import { chapter as ch10 } from "./chapters/10-the-interbank-network";
import { chapter as ch11 } from "./chapters/11-payment-schemes";
import { chapter as ch12 } from "./chapters/12-sepa";
import { chapter as ch13 } from "./chapters/13-card-transactions";
import { chapter as ch14 } from "./chapters/14-snapshots-audit-and-statements";
import { chapter as ch15 } from "./chapters/15-persistence-and-durability";

/** Network-level explorer routes a question may deep-link to. */
export const EXPLORE_ROUTES = [
  "/",
  "/payments",
  "/mandates",
  "/cycles",
  "/settlements",
  "/central-bank",
  "/schemes",
] as const;

export const chapters: Chapter[] = [
  ch01, ch02, ch03, ch04, ch05, ch06, ch07,
  ch08, ch09, ch10, ch11, ch12, ch13, ch14,
  ch15,
];

export function getChapter(slug: string): Chapter | undefined {
  return chapters.find((c) => c.slug === slug);
}

export function allQuestions(): Question[] {
  return chapters.flatMap((c) => c.questions);
}

export function mixedQuestions(): Question[] {
  return allQuestions();
}

export interface Part {
  name: string;
  chapters: Chapter[];
}

/** Chapters grouped by their `part`, preserving chapter order. */
export function chaptersByPart(): Part[] {
  const parts: Part[] = [];
  for (const c of chapters) {
    let p = parts.find((x) => x.name === c.part);
    if (!p) {
      p = { name: c.part, chapters: [] };
      parts.push(p);
    }
    p.chapters.push(c);
  }
  return parts;
}
