import { describe, expect, it } from "vitest";

import { copyToRead, formatSize, holderKey, holderOf, shortDefinition } from "./message";
import type { Crossing, Institution, Participant } from "./types";

describe("shortDefinition", () => {
  it("keeps the family and the variant and drops the version", () => {
    expect(shortDefinition("pacs.008.001.10")).toBe("pacs.008");
    expect(shortDefinition("camt.053.001.08")).toBe("camt.053");
  });

  it("answers whole what it cannot split, and names a file with no header", () => {
    expect(shortDefinition("HRD")).toBe("HRD");
    expect(shortDefinition("")).toBe("file");
  });
});

describe("formatSize", () => {
  it("counts bytes below a kilobyte and kilobytes above one", () => {
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(1023)).toBe("1023 B");
    expect(formatSize(1024)).toBe("1.0 kB");
    expect(formatSize(4300)).toBe("4.2 kB");
  });
});

const CB = "CBSEDEFFXXX";
const CSM = "CSMXFRPPXXX";
const AURO = "AURODEFFXXX";
const VERD = "VERDITMMXXX";

const institutions: Institution[] = [
  { bic: CB, name: "Central bank", role: "settlement agent" },
  { bic: CSM, name: "Clearing house", role: "clearing house" },
  { bic: AURO, name: "Aurora Bank", role: "member bank" },
  { bic: VERD, name: "Banca Verde", role: "member bank" },
];

// A bank's own id is not its address by necessity, so the resolver looks it up.
const participants = [{ id: "aurora", bic: AURO }] as Participant[];

function crossing(over: Partial<Crossing> = {}): Crossing {
  return {
    from: CB,
    to: AURO,
    msgDefIdr: "camt.053.001.08",
    msgId: "CBSEDEFFXXX-4",
    payments: [],
    payloadSize: 1800,
    ...over,
  };
}

describe("holderOf", () => {
  it("sends each part to its own listener", () => {
    expect(holderOf(CSM, institutions, participants)).toEqual({ kind: "clearing house" });
    expect(holderOf(CB, institutions, participants)).toEqual({ kind: "settlement agent" });
    expect(holderOf(AURO, institutions, participants)).toEqual({ kind: "bank", pid: "aurora" });
  });

  it("answers nobody for an address the mesh does not list", () => {
    expect(holderOf("NOSUCHXXXXX", institutions, participants)).toBeNull();
  });

  it("answers nobody for a bank the deployment holds no row for", () => {
    // Verde is drawn — it has a database — and there is no participant row to
    // take its listener from, so its log cannot be reached rather than being
    // read off an address that happens to look like an id.
    expect(holderOf(VERD, institutions, participants)).toBeNull();
  });
});

describe("holderKey", () => {
  it("tells two listeners apart, which is what a seq needs qualifying by", () => {
    const keys = [
      holderKey({ kind: "clearing house" }),
      holderKey({ kind: "settlement agent" }),
      holderKey({ kind: "bank", pid: "aurora" }),
      holderKey({ kind: "bank", pid: "verde" }),
    ];
    expect(new Set(keys).size).toBe(keys.length);
  });
});

describe("copyToRead", () => {
  it("prefers the sender's copy, which is the file as it was sent", () => {
    expect(copyToRead(crossing({ sentSeq: 4, receivedSeq: 9 }))).toEqual({
      bic: CB,
      seq: 4,
      direction: "sent",
    });
  });

  it("falls back to the recipient's when the sender recorded nothing", () => {
    expect(copyToRead(crossing({ receivedSeq: 9 }))).toEqual({
      bic: AURO,
      seq: 9,
      direction: "received",
    });
  });

  it("opens nothing when neither end recorded it", () => {
    expect(copyToRead(crossing())).toBeNull();
  });
});
