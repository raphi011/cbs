import { describe, expect, it } from "vitest";

import { formatNumericAnswer, unitLabel } from "./units";
import { chapters } from ".";

describe("unitLabel", () => {
  it("names an asset's major and minor units", () => {
    expect(unitLabel({ asset: "USD", in: "major" })).toBe("dollars");
    expect(unitLabel({ asset: "USD", in: "minor" })).toBe("cents");
    expect(unitLabel({ asset: "EUR", in: "major" })).toBe("euros");
    expect(unitLabel({ asset: "EUR", in: "minor" })).toBe("cents");
  });

  // The whole reason this type changed: `"cents" | "dollars"` had no correct
  // label for an answer in satoshi, and no way to add one.
  it("names bitcoin's units", () => {
    expect(unitLabel({ asset: "BTC", in: "major" })).toBe("bitcoin");
    expect(unitLabel({ asset: "BTC", in: "minor" })).toBe("satoshi");
  });
});

describe("formatNumericAnswer", () => {
  // Unchanged from the old renderer, which special-cased dollars and printed
  // everything else bare.
  it("prefixes a major-unit amount with the asset's symbol", () => {
    expect(formatNumericAnswer(100, { asset: "USD", in: "major" })).toBe("$100");
    expect(formatNumericAnswer(25, { asset: "EUR", in: "major" })).toBe("€25");
  });

  it("names the unit where the asset has no symbol", () => {
    expect(formatNumericAnswer(1, { asset: "BTC", in: "major" })).toBe("1 bitcoin");
    expect(formatNumericAnswer(200000, { asset: "BTC", in: "minor" })).toBe("200000 satoshi");
    expect(formatNumericAnswer(2500, { asset: "EUR", in: "minor" })).toBe("2500 cents");
  });

  it("prints a bare number when the question carries no unit", () => {
    expect(formatNumericAnswer(3)).toBe("3");
  });

  // The number is the answer as the question states it, never rescaled — a
  // question asking "in cents" wants 2500 back, not "25.00".
  it("does not rescale a minor-unit answer", () => {
    expect(formatNumericAnswer(2500, { asset: "EUR", in: "minor" })).toContain("2500");
  });
});

// Every numeric question in the bank must render both of the above. This is the
// gate that catches a chapter written against the old string shape, or one that
// names an asset the label table does not know.
describe("the question bank", () => {
  it("labels and formats every numeric question that carries a unit", () => {
    const numeric = chapters
      .flatMap((ch) => ch.questions)
      .filter((q) => q.kind === "numeric");
    expect(numeric.length).toBeGreaterThan(0);
    for (const q of numeric) {
      if (!q.unit) continue;
      expect(unitLabel(q.unit), q.id).not.toBe("");
      expect(formatNumericAnswer(q.answer, q.unit), q.id).toContain(String(q.answer));
    }
  });
});
