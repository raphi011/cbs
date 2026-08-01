import { existsSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { homeFor, navFor, type Identity } from "./identity";
import { EXPLORE_ROUTES } from "./quiz";

// vitest's cwd is web/, so this is web/src/app.
const APP_DIR = join(process.cwd(), "src", "app");

// Sentinels navFor interpolates, mapped back to the dynamic segment they stand
// for. Real ids would name no file.
const PID = "__PID__";
const DID = "__DID__";

const IDENTITIES: Identity[] = [
  { persona: "central-bank" },
  { persona: "clearing-house" },
  { persona: "bank", pid: PID },
  { persona: "customer", pid: PID, did: DID },
];

// Routes a later task builds. Each line is deleted by the task that creates its
// page, so this list is empty now that 6b's last pending route (the customer's
// home, Task 12) has one — an entry that outlives its task is a nav link
// pointing at nothing, which is what this file exists to reject.
const PENDING = new Set<string>([]);

// An App Router route renders src/app/<segments>/page.tsx. A dynamic segment is
// a directory literally named [pid] / [did].
function routeFileFor(href: string): string {
  const segments = href
    .split("/")
    .filter(Boolean)
    .map((s) => (s === PID ? "[pid]" : s === DID ? "[did]" : s));
  return join(APP_DIR, ...segments, "page.tsx");
}

describe("nav integrity", () => {
  // The route move is the riskiest mechanical part of the persona work, and a
  // dead nav link is invisible to typecheck, lint and `next build`.
  it("every nav href resolves to a real page", () => {
    for (const identity of IDENTITIES) {
      for (const item of navFor(identity)) {
        if (PENDING.has(item.href)) continue;
        expect(
          existsSync(routeFileFor(item.href)),
          `${identity.persona} nav "${item.label}" → ${item.href}`,
        ).toBe(true);
      }
    }
  });

  it("every persona's home resolves to a real page", () => {
    for (const identity of IDENTITIES) {
      const href = homeFor(identity);
      if (PENDING.has(href)) continue;
      expect(existsSync(routeFileFor(href)), `home for ${identity.persona} → ${href}`).toBe(
        true,
      );
    }
  });

  // A hole a task forgot to close is worse than one that was never dug: once
  // Task 9 or Task 12 lands, its line in PENDING is exempting a route that now
  // resolves, and the exemption above would hide a *real* dead link from then
  // on. This fails the day that happens, forcing the line's deletion rather
  // than letting it rot into a permanent carve-out.
  it("every PENDING entry is still unbuilt", () => {
    for (const href of PENDING) {
      expect(existsSync(routeFileFor(href)), `PENDING entry ${href} now resolves — delete it`).toBe(
        false,
      );
    }
  });

  // The quiz deep-links into the explorer from 40 questions across eight
  // chapters, 33 of which move here. index.test.ts already pins that every
  // question's explore.href is in this allowlist, and narrowing the type makes
  // tsc do the same; nothing pinned that an allowlisted route *exists*.
  it("every allowlisted quiz explore route resolves to a real page", () => {
    for (const href of EXPLORE_ROUTES) {
      const file = href === "/" ? join(APP_DIR, "page.tsx") : routeFileFor(href);
      expect(existsSync(file), `EXPLORE_ROUTES entry ${href}`).toBe(true);
    }
  });
});
