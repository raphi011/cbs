import { describe, expect, it } from "vitest";

import { conceptUrlTransform, validateConceptContent } from "./concept-links";
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
// store/pg/schema/0001_init.sql) are kept in step by hand, so a claim that
// drifts is caught by a reader or by nothing. This one had drifted: the prose
// said the balance is "signed by normal balance" and the SQL beneath it
// hardcoded `direction = debit`, which negates every liability balance —
// including the README's own worked example, where Alice's checking account is
// a liability asserted at 75000 and the published query returns −75000. The
// real query parameterises the direction (store/pg/tx_ledger.go, `$3 = normal`)
// and quiz ch15-q7 describes it correctly, so three layers agreed and one did
// not.
describe("derived-balance hint", () => {
  const body = hintContent["derived-balance"].body;

  it("does not hardcode the debit direction in the balance query", () => {
    expect(body).not.toMatch(/direction\s*=\s*debit/i);
  });

  it("shows the direction as a parameter, matching store/pg", () => {
    expect(body).toMatch(/direction\s*=\s*\$3/);
  });
});
