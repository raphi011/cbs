import { describe, expect, it } from "vitest";

import { backendConfig, bankUrl, institutionUrl } from "./backend-url";

const DEFAULTS = backendConfig({});

describe("institutionUrl", () => {
  // Mirrors cmd/server's plan(): the central bank takes the base port and the
  // clearing house the next, so `make dev` needs no configuration at all.
  it("puts the two institutions at the base port and the next", () => {
    expect(institutionUrl("central-bank", DEFAULTS)).toBe("http://localhost:8081");
    expect(institutionUrl("clearing-house", DEFAULTS)).toBe("http://localhost:8082");
  });

  it("honours an override", () => {
    const cfg = backendConfig({ BACKENDS: '{"central-bank":"http://cb.internal:9000"}' });
    expect(institutionUrl("central-bank", cfg)).toBe("http://cb.internal:9000");
    expect(institutionUrl("clearing-house", cfg)).toBe("http://localhost:8082");
  });

  it("honours a moved base port and host", () => {
    const cfg = backendConfig({ BASE_PORT: "9000", BACKEND_HOST: "http://api" });
    expect(institutionUrl("central-bank", cfg)).toBe("http://api:9000");
  });
});

describe("bankUrl", () => {
  // The seed's ids are bank_1, bank_3, bank_5, bank_7 — deliberately not
  // contiguous. Nothing may infer a port from an id, only from roster position.
  const roster = ["bank_1", "bank_3", "bank_5", "bank_7"];

  it("derives a bank's port from its roster position, never from its id", () => {
    expect(bankUrl("bank_1", roster, DEFAULTS)).toBe("http://localhost:8083");
    expect(bankUrl("bank_3", roster, DEFAULTS)).toBe("http://localhost:8084");
    expect(bankUrl("bank_5", roster, DEFAULTS)).toBe("http://localhost:8085");
    expect(bankUrl("bank_7", roster, DEFAULTS)).toBe("http://localhost:8086");
  });

  it("honours an override keyed by participant id", () => {
    const cfg = backendConfig({ BACKENDS: '{"bank_5":"http://verde:7000"}' });
    expect(bankUrl("bank_5", roster, cfg)).toBe("http://verde:7000");
  });

  // Admission is not provisioning: a bank created at runtime is in the roster
  // and has no listener until the server restarts. Saying so is the whole point
  // — the alternative is a console whose every request 502s.
  it("refuses a bank that is not in the roster, and says why", () => {
    expect(() => bankUrl("bank_99", roster, DEFAULTS)).toThrow(/admission is not provisioning/i);
  });
});
