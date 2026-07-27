import { describe, expect, it } from "vitest";

import { chapters, allQuestions, getChapter, chaptersByPart, EXPLORE_ROUTES } from "./index";
import { hintContent } from "@/components/hint-content";

const BOOK_SLUGS = [
  "01-what-a-bank-is",
  "02-double-entry-bookkeeping",
  "03-the-chart-of-accounts",
  "04-ledgers-subledgers-and-money",
  "05-transactions-and-postings",
  "06-booking-date-vs-value-date",
  "07-balances-and-holds",
  "08-account-lifecycle-and-overdraft",
  "09-clearing-and-settlement",
  "10-the-interbank-network",
  "11-payment-schemes",
  "12-sepa",
  "13-card-transactions",
  "14-snapshots-audit-and-statements",
  "15-persistence-and-durability",
  "16-multi-asset-accounting",
];

describe("quiz bank", () => {
  it("has the 16 chapters, in order, with unique known slugs", () => {
    expect(chapters).toHaveLength(16);
    expect(chapters.map((c) => c.number)).toEqual(Array.from({ length: 16 }, (_, i) => i + 1));
    const slugs = chapters.map((c) => c.slug);
    expect(new Set(slugs).size).toBe(slugs.length);
    for (const s of slugs) expect(BOOK_SLUGS).toContain(s);
  });

  it("resolves a chapter by slug", () => {
    expect(getChapter("02-double-entry-bookkeeping")?.number).toBe(2);
    expect(getChapter("nope")).toBeUndefined();
  });

  it("groups every chapter under exactly one part", () => {
    const grouped = chaptersByPart().flatMap((p) => p.chapters);
    expect(grouped).toHaveLength(16);
  });

  it("has globally unique question ids", () => {
    const ids = allQuestions().map((q) => q.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("has well-formed questions", () => {
    for (const q of allQuestions()) {
      expect(q.prompt.trim().length).toBeGreaterThan(0);
      expect(q.explanation.trim().length).toBeGreaterThan(0);
      if (q.kind === "mc") {
        expect(q.options.length).toBeGreaterThanOrEqual(2);
        expect(q.answer).toBeGreaterThanOrEqual(0);
        expect(q.answer).toBeLessThan(q.options.length);
      } else if (q.kind === "multi") {
        expect(q.options.length).toBeGreaterThanOrEqual(2);
        expect(q.answers.length).toBeGreaterThanOrEqual(1);
        expect(new Set(q.answers).size).toBe(q.answers.length);
        for (const a of q.answers) {
          expect(a).toBeGreaterThanOrEqual(0);
          expect(a).toBeLessThan(q.options.length);
        }
      } else if (q.kind === "numeric") {
        expect(Number.isFinite(q.answer)).toBe(true);
      } else {
        expect(typeof q.answer).toBe("boolean");
      }
      if (q.concept) expect(q.concept in hintContent).toBe(true);
      if (q.explore) expect(EXPLORE_ROUTES).toContain(q.explore.href);
    }
  });

  // A dangling [[link]] in a hint body throws at runtime and takes every route
  // in the dev app with it; one in a *question explanation* is quieter but just
  // as wrong — the link renders with the raw key as its label and opens a panel
  // for a concept that does not exist. Neither is caught by typecheck, lint or
  // `next build`, so it is caught here.
  it("resolves every [[wiki-link]] in a question explanation", () => {
    const LINK_RE = /\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g;
    const broken: string[] = [];
    for (const q of allQuestions()) {
      for (const m of q.explanation.matchAll(LINK_RE)) {
        const key = m[1].trim();
        if (!(key in hintContent)) broken.push(`${q.id} → [[${key}]]`);
      }
    }
    expect(broken).toEqual([]);
  });

  it("ships at least the Chapter 2 exemplar", () => {
    expect(getChapter("02-double-entry-bookkeeping")!.questions.length).toBeGreaterThanOrEqual(8);
  });
});
