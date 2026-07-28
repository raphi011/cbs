import { describe, expect, it } from "vitest";

import { formatRate, parseRatePercent } from "./rate";

describe("formatRate", () => {
  it("renders a millionths rate at its own scale, not a hardcoded 1e6", () => {
    expect(formatRate(150_000, 1_000_000)).toBe("15.00%");
    expect(formatRate(20_000, 1_000_000)).toBe("2.00%");
  });

  it("would render wrong at a guessed scale — the bug this replaces", () => {
    // If a caller divided by a hardcoded 1_000_000 instead of the response's
    // own rateScale, a facility priced on a hypothetical 100-scale rate would
    // render 1500x too small. formatRate takes the scale as a parameter so
    // that mistake isn't representable.
    expect(formatRate(15, 100)).toBe("15.00%");
  });
});

describe("parseRatePercent", () => {
  it("converts a typed percentage to the millionths integer the API expects", () => {
    expect(parseRatePercent("15")).toBe(150_000);
    expect(parseRatePercent("2.5")).toBe(25_000);
  });

  it("returns null on empty or non-numeric input", () => {
    expect(parseRatePercent("")).toBeNull();
    expect(parseRatePercent("  ")).toBeNull();
    expect(parseRatePercent("abc")).toBeNull();
  });
});
