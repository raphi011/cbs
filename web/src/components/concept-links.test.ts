import { describe, expect, it } from "vitest";

import {
  conceptUrlTransform,
  parseConceptLinks,
  preprocessConceptMarkdown,
  validateConceptContent,
} from "./concept-links";
import { chapters } from "@/lib/quiz";
import { hintContent, type HintEntry } from "./hint-content";

describe("conceptUrlTransform", () => {
  // The whole point: react-markdown's default sanitizer drops the custom
  // `concept:` scheme (→ ""), which is what broke the panel's wiki-links.
  it("passes concept: links through untouched", () => {
    expect(conceptUrlTransform("concept:deposit-account")).toBe(
      "concept:deposit-account",
    );
  });

  it("keeps internal paths", () => {
    expect(conceptUrlTransform("/participants/p_1")).toBe("/participants/p_1");
  });

  it("keeps http(s) links", () => {
    expect(conceptUrlTransform("https://example.com")).toBe(
      "https://example.com",
    );
  });

  // Must NOT regress react-markdown's XSS protection for unknown schemes.
  it("still strips dangerous schemes", () => {
    expect(conceptUrlTransform("javascript:alert(1)")).toBe("");
  });
});

describe("preprocessConceptMarkdown", () => {
  it("rewrites a wiki-link to a concept: markdown link, titled from the registry", () => {
    expect(preprocessConceptMarkdown("See [[netting]].")).toBe(
      `See [${hintContent["netting"].title}](concept:netting).`,
    );
  });

  it("honours an explicit label", () => {
    expect(preprocessConceptMarkdown("See [[netting|the net]].")).toBe(
      "See [the net](concept:netting).",
    );
  });

  // The defect: one regex rewrote every [[…]] in a body, including the ones in
  // fenced blocks, so the clearing-vs-settlement hint printed the literal string
  // "[Clearing suspense](concept:clearing-suspense)" in monospace.
  it("leaves a wiki-link inside a fenced block exactly as written", () => {
    const body = "Before [[netting]].\n\n```\nBanks clear [[clearing-suspense]]\n```\n\nAfter.";
    const out = preprocessConceptMarkdown(body);
    expect(out).toContain("```\nBanks clear [[clearing-suspense]]\n```");
    expect(out).toContain("Before [Netting](concept:netting).");
  });

  it("leaves a wiki-link inside an inline code span alone", () => {
    expect(preprocessConceptMarkdown("Write `[[netting]]` to link it.")).toBe(
      "Write `[[netting]]` to link it.",
    );
  });

  it("resumes rewriting after the fence closes", () => {
    const body = "```\n[[netting]]\n```\nthen [[netting]]";
    expect(preprocessConceptMarkdown(body)).toBe(
      "```\n[[netting]]\n```\nthen [Netting](concept:netting)",
    );
  });

  // An unterminated fence must not swallow the rest of the body — the links
  // after it should still work, i.e. degrade to the old behaviour.
  it("does not swallow the body when a fence is never closed", () => {
    expect(preprocessConceptMarkdown("```\nopen forever [[netting]]")).toBe(
      "```\nopen forever [Netting](concept:netting)",
    );
  });
});

describe("parseConceptLinks", () => {
  it("collects the distinct keys a body links to", () => {
    expect(parseConceptLinks("[[netting]] and [[netting|again]] and [[reserve-account]]")).toEqual([
      "netting",
      "reserve-account",
    ]);
  });

  // The "Related" row lists what the reader can click; a key that renders as
  // literal text in a code block is not clickable.
  it("ignores keys that only appear inside a code region", () => {
    expect(parseConceptLinks("```\n[[netting]]\n```")).toEqual([]);
  });
});

describe("validateConceptContent", () => {
  // The registry's own integrity. This guard exists, but it only runs from
  // ConceptPanelProvider's mount effect and only when NODE_ENV !== production —
  // so a dangling [[link]] passes typecheck, lint and `next build`, and then
  // crashes every route of the dev app with a full-screen runtime error. That
  // is exactly how one shipped. Running it here is what makes the guard part of
  // the gate rather than something a developer discovers by loading the app.
  it("finds no dangling wiki-links in the registry", () => {
    expect(() => validateConceptContent()).not.toThrow();
  });

  // …and the assertion above is only worth anything if the guard can fail.
  it("throws when a body links to a key that is not registered", () => {
    const registry = hintContent as unknown as Record<string, HintEntry>;
    registry["__probe__"] = {
      title: "Probe",
      body: "Links to [[definitely-not-a-concept]].",
    };
    try {
      expect(() => validateConceptContent()).toThrow(
        /definitely-not-a-concept/,
      );
    } finally {
      delete registry["__probe__"];
    }
    expect(() => validateConceptContent()).not.toThrow();
  });

  // …but only for links that render. A key quoted inside a code block is never
  // resolved by the rewrite, so it cannot throw, and failing an author for
  // showing wiki-link syntax in an example would be a false positive.
  it("ignores a dangling link inside a code region", () => {
    expect(() =>
      validateConceptContent([
        { source: "ch99/q1", body: "```\n[[definitely-not-a-concept]]\n```" },
      ]),
    ).not.toThrow();
  });

  // The gap this test closes: the runtime guard scans hint bodies only, but
  // quiz explanations carry wiki-links too. A dangling one there passed
  // typecheck, lint, `next build` AND `npm run test`, and surfaced only when
  // someone answered that particular question.
  it("finds no dangling wiki-links in any quiz explanation", () => {
    const sources = chapters.flatMap((ch) =>
      ch.questions.map((q) => ({
        source: `${ch.slug}/${q.id}`,
        body: q.explanation,
      })),
    );
    expect(sources.length).toBeGreaterThan(0);
    expect(() => validateConceptContent(sources)).not.toThrow();
  });

  // …and that assertion is only worth anything if a bad chapter link fails.
  it("throws when a quiz explanation links to a key that is not registered", () => {
    expect(() =>
      validateConceptContent([
        { source: "ch99/q1", body: "See [[definitely-not-a-concept]]." },
      ]),
    ).toThrow(/ch99\/q1 → \[\[definitely-not-a-concept\]\]/);
  });
});

// The four documentation layers (README.md, this registry, the quiz, and
// store/sqlite/schema/{bank,csm,centralbank}/0001_init.sql) are kept in step by hand, so a claim that
// drifts is caught by a reader or by nothing. This one had drifted: the prose
// said the balance is "signed by normal balance" and the SQL beneath it
// hardcoded `direction = debit`, which negates every liability balance —
// including the README's own worked example, where Alice's checking account is
// a liability asserted at 75000 and the published query returns −75000. The
// real query parameterises the direction (store/sqlite/tx_ledger.go, where the
// first `?` is the normal direction) and quiz ch15-q7 describes it correctly, so
// three layers agreed and one did not.
describe("derived-balance hint", () => {
  const body = hintContent["derived-balance"].body;

  it("does not hardcode the debit direction in the balance query", () => {
    expect(body).not.toMatch(/direction\s*=\s*debit/i);
  });

  it("shows the direction as a parameter, matching store/sqlite", () => {
    expect(body).toMatch(/direction\s*=\s*\?/);
  });
});
