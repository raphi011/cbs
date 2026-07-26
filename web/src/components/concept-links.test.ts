import { describe, expect, it } from "vitest";

import { conceptUrlTransform, validateConceptContent } from "./concept-links";
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
});
