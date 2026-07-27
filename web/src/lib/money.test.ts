import { describe, expect, it } from "vitest";

import {
  amountToInput,
  formatAmount,
  formatSigned,
  parseAmount,
} from "./money";

const EUR = { code: "EUR", scale: 2 };
const BTC = { code: "BTC", scale: 8 };
const JPY = { code: "JPY", scale: 0 };

describe("formatAmount", () => {
  it("formats a 2-decimal asset", () => {
    expect(formatAmount(12345, { code: "EUR", scale: 2 })).toBe("123.45");
  });

  it("formats an 8-decimal asset", () => {
    // The bug this replaces: dividing by 100 renders 1 BTC as 1,000,000.00.
    expect(formatAmount(100_000_000, { code: "BTC", scale: 8 })).toBe("1.00000000");
  });

  it("formats a 0-decimal asset", () => {
    expect(formatAmount(500, { code: "JPY", scale: 0 })).toBe("500");
  });
});

describe("formatSigned", () => {
  it("adds an explicit sign at the asset's scale", () => {
    expect(formatSigned(2500, EUR)).toBe("+25.00");
    expect(formatSigned(-2500, EUR)).toBe("-25.00");
    expect(formatSigned(0, EUR)).toBe("0.00");
  });

  it("signs an 8-decimal asset the same way", () => {
    expect(formatSigned(-100_000_000, BTC)).toBe("-1.00000000");
  });
});

describe("parseAmount", () => {
  it("converts a major-unit string to an integer amount at the asset's scale", () => {
    expect(parseAmount("30", EUR)).toBe(3000);
    expect(parseAmount("30.5", EUR)).toBe(3050);
    expect(parseAmount("30.00", EUR)).toBe(3000);
  });

  it("scales an 8-decimal asset instead of assuming cents", () => {
    expect(parseAmount("1", BTC)).toBe(100_000_000);
    expect(parseAmount("0.00000001", BTC)).toBe(1);
  });

  it("returns null on empty or non-numeric input", () => {
    expect(parseAmount("", EUR)).toBeNull();
    expect(parseAmount("   ", EUR)).toBeNull();
    expect(parseAmount("abc", EUR)).toBeNull();
  });

  it("has nothing to round for a 0-decimal asset", () => {
    expect(parseAmount("500", JPY)).toBe(500);
  });
});

describe("amountToInput", () => {
  it("renders an editable major-unit string at the asset's scale", () => {
    expect(amountToInput(3000, EUR)).toBe("30.00");
    expect(amountToInput(100_000_000, BTC)).toBe("1.00000000");
    expect(amountToInput(500, JPY)).toBe("500");
  });
});
