import { describe, expect, it } from "vitest";

import { checkDigitsPass, compactIban, groupIban } from "./iban";

// Published examples, one per country this system issues in, plus one the seed
// mints. Hand-checkable: each is what the backend's minter produces.
const VALID = [
  "DE89370400440532013000",
  "IT60X0542811101000000123456",
  "SE4550000000058398257466",
  "FR1420041010050500013M02606",
  "DE20999000010000000001",
];

describe("checkDigitsPass", () => {
  it("accepts a real IBAN in every country this system issues in", () => {
    for (const iban of VALID) {
      expect(checkDigitsPass(iban), iban).toBe(true);
    }
  });

  it("accepts the spellings a person types", () => {
    expect(checkDigitsPass("DE20 9990 0001 0000 0000 01")).toBe(true);
    expect(checkDigitsPass("DE20-9990-0001-0000-0000-01")).toBe(true);
    expect(checkDigitsPass("de20999000010000000001")).toBe(true);
  });

  it("catches every single-digit substitution", () => {
    // The property the check digits are for, measured rather than asserted:
    // one wrong digit anywhere in the address, in every position, is caught.
    const iban = "DE20999000010000000001";
    let checked = 0;
    for (let i = 2; i < iban.length; i++) {
      for (const d of "0123456789") {
        if (d === iban[i]) continue;
        const typo = iban.slice(0, i) + d + iban.slice(i + 1);
        expect(checkDigitsPass(typo), typo).toBe(false);
        checked++;
      }
    }
    expect(checked).toBe(180);
  });

  it("catches a transposition at any distance, not only adjacent ones", () => {
    // 10 has multiplicative order 96 modulo 97, so no address short of 96
    // characters can hide a swapped pair.
    const iban = "DE20999000010000000001";
    for (let i = 2; i < iban.length; i++) {
      for (let j = i + 1; j < iban.length; j++) {
        if (iban[i] === iban[j]) continue;
        const chars = [...iban];
        [chars[i], chars[j]] = [chars[j], chars[i]];
        expect(checkDigitsPass(chars.join("")), chars.join("")).toBe(false);
      }
    }
  });

  it("refuses anything that is not shaped like an IBAN", () => {
    for (const bad of ["", "DE20", "20DE999000010000000001", "DE2X999000010000000001", "DE20/9990/0001"]) {
      expect(checkDigitsPass(bad), bad).toBe(false);
    }
  });

  it("does not claim the address exists", () => {
    // A well-formed address nobody holds passes here and is refused by the bank
    // that finds no such account. That is the division of labour, not a gap.
    expect(checkDigitsPass("DE59999000010009999999")).toBe(true);
  });
});

describe("compactIban", () => {
  it("strips separators and folds case", () => {
    expect(compactIban("de20 9990-0001 0000 0000 01")).toBe("DE20999000010000000001");
  });
});

describe("groupIban", () => {
  it("prints an address the way a statement does", () => {
    expect(groupIban("DE20999000010000000001")).toBe("DE20 9990 0001 0000 0000 01");
  });
});
