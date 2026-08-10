import { describe, expect, it } from "vitest";

import {
  backendFor,
  homeFor,
  identityFromPathname,
  navFor,
  type Identity,
} from "./identity";

describe("identityFromPathname", () => {
  it("reads the two institutions off their prefixes", () => {
    expect(identityFromPathname("/central-bank")).toEqual({ persona: "central-bank" });
    expect(identityFromPathname("/central-bank/audit")).toEqual({ persona: "central-bank" });
    expect(identityFromPathname("/clearing-house")).toEqual({ persona: "clearing-house" });
    expect(identityFromPathname("/clearing-house/settlements/set_1")).toEqual({
      persona: "clearing-house",
    });
  });

  it("reads a bank and its pid", () => {
    expect(identityFromPathname("/bank/bank_1")).toEqual({ persona: "bank", pid: "bank_1" });
    expect(identityFromPathname("/bank/bank_1/ledger")).toEqual({
      persona: "bank",
      pid: "bank_1",
    });
  });

  it("reads a customer as a (bank, account) pair", () => {
    expect(identityFromPathname("/customer/bank_1/dep_9")).toEqual({
      persona: "customer",
      pid: "bank_1",
      did: "dep_9",
    });
    expect(identityFromPathname("/customer/bank_1/dep_9/send")).toEqual({
      persona: "customer",
      pid: "bank_1",
      did: "dep_9",
    });
  });

  // The two null cases the design names: the lobby and Learn sit outside the
  // persona system entirely.
  it("has no identity at the lobby or under Learn", () => {
    expect(identityFromPathname("/")).toBeNull();
    expect(identityFromPathname("/learn")).toBeNull();
    expect(identityFromPathname("/learn/12-sepa")).toBeNull();
    expect(identityFromPathname("/learn/mixed")).toBeNull();
  });

  // A prefix without its context addresses nothing, so it is not an identity.
  // Landing on /bank with no pid must not produce { pid: undefined }.
  it("refuses a persona prefix with its context missing", () => {
    expect(identityFromPathname("/bank")).toBeNull();
    expect(identityFromPathname("/customer/bank_1")).toBeNull();
  });

  it("ignores a trailing slash", () => {
    expect(identityFromPathname("/bank/bank_1/")).toEqual({ persona: "bank", pid: "bank_1" });
  });
});

const ALL: Identity[] = [
  { persona: "central-bank" },
  { persona: "clearing-house" },
  { persona: "bank", pid: "bank_1" },
  { persona: "customer", pid: "bank_1", did: "dep_9" },
];

describe("homeFor", () => {
  it("sends each identity to its own root", () => {
    expect(homeFor({ persona: "central-bank" })).toBe("/central-bank");
    expect(homeFor({ persona: "clearing-house" })).toBe("/clearing-house");
    expect(homeFor({ persona: "bank", pid: "bank_1" })).toBe("/bank/bank_1");
    expect(homeFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toBe(
      "/customer/bank_1/dep_9",
    );
  });

  // homeFor is what the identity picker navigates to, so it must round-trip:
  // the identity you pick is the identity the destination reads back.
  it("round-trips through identityFromPathname", () => {
    for (const it of ALL) {
      expect(identityFromPathname(homeFor(it))).toEqual(it);
    }
  });
});

describe("backendFor", () => {
  it("gives each institution its own listener", () => {
    expect(backendFor({ persona: "central-bank" })).toBe("central-bank");
    expect(backendFor({ persona: "clearing-house" })).toBe("clearing-house");
  });

  it("gives a bank its own", () => {
    expect(backendFor({ persona: "bank", pid: "bank_1" })).toBe("bank/bank_1");
  });

  // The asymmetry is the point. Three of the four personas are institutions with
  // a listener; a customer is a *view onto* a bank and talks to their bank's,
  // which is what a retail app does — it has no CSM connection and no backend of
  // its own.
  it("sends a customer to their bank's listener and not one of their own", () => {
    expect(backendFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toBe(
      backendFor({ persona: "bank", pid: "bank_1" }),
    );
    expect(backendFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).not.toContain(
      "dep_9",
    );
  });
});

describe("navFor", () => {
  it("gives the central bank the settlement layer and nothing else", () => {
    expect(navFor({ persona: "central-bank" }).map((n) => n.href)).toEqual([
      "/central-bank",
      "/central-bank/audit",
    ]);
  });

  it("gives the clearing house the network", () => {
    expect(navFor({ persona: "clearing-house" }).map((n) => n.href)).toEqual([
      "/clearing-house",
      "/clearing-house/payments",
      "/clearing-house/cycles",
      "/clearing-house/settlements",
      "/clearing-house/schemes",
      "/clearing-house/directory",
    ]);
  });

  // The whole point of the split: no screen showing an individual payment is on
  // the central bank's console, because a real central bank does not see one.
  it("keeps individual payments off the central bank's console", () => {
    for (const item of navFor({ persona: "central-bank" })) {
      expect(item.href).not.toContain("payments");
    }
  });

  it("scopes every bank entry to the bank's own pid", () => {
    const nav = navFor({ persona: "bank", pid: "bank_1" });
    expect(nav.map((n) => n.href)).toEqual([
      "/bank/bank_1",
      "/bank/bank_1/payments",
      // A mandate is the CREDITOR's bank's row, so the console that shows one
      // is a bank's. It was the clearing house's until this moved.
      "/bank/bank_1/mandates",
      // A bank's own copy of the scheme's directory, which is a different
      // screen from the clearing house's entry of the same name.
      "/bank/bank_1/directory",
      "/bank/bank_1/ledger",
      "/bank/bank_1/transactions",
      "/bank/bank_1/facilities",
      "/bank/bank_1/audit",
      "/bank/bank_1/deposit-audit",
    ]);
    for (const item of nav) {
      expect(item.href.startsWith("/bank/bank_1")).toBe(true);
    }
  });

  // Only the home entry may match exactly; every other entry is a section
  // prefix, so a detail page below it keeps its parent highlighted.
  it("marks exactly one entry per persona as an exact match", () => {
    for (const identity of ALL) {
      const nav = navFor(identity);
      if (nav.length === 0) continue;
      expect(nav.filter((n) => n.exact)).toHaveLength(1);
      expect(nav[0].exact).toBe(true);
      expect(nav[0].href).toBe(homeFor(identity));
    }
  });

  // In the order the tab strip shows them.
  it("gives a customer the screens they have, all under their own account", () => {
    const nav = navFor({ persona: "customer", pid: "bank_1", did: "dep_9" });
    expect(nav.map((n) => n.href)).toEqual([
      "/customer/bank_1/dep_9",
      "/customer/bank_1/dep_9/send",
      "/customer/bank_1/dep_9/activity",
    ]);
  });
});
