import { describe, expect, it } from "vitest";

import { conceptUrlTransform } from "./concept-links";

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
