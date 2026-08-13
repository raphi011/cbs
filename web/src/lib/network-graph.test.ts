import { describe, expect, it } from "vitest";

import {
  CANVAS,
  codeOf,
  forReading,
  isResting,
  layout,
  restingByWire,
  wireKey,
} from "./network-graph";
import type { Crossing, Institution, Wire } from "./types";

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

// Aurora is admitted and Verde is not, which is the difference this drawing has
// to show: Verde holds a database and is reachable by nobody.
const wires: Wire[] = [
  { subscriber: CSM, host: CB },
  { subscriber: AURO, host: CSM },
  { subscriber: AURO, host: CB },
];

function crossing(from: string, to: string, took: boolean): Crossing {
  return {
    from,
    to,
    msgDefIdr: "pacs.008.001.10",
    msgId: `${from}-${to}`,
    orderId: `O${from}${to}`,
    sentAt: "2025-09-15T10:00:00Z",
    receivedAt: took ? "2025-09-15T10:05:00Z" : undefined,
    payments: [],
    payloadSize: 1200,
  };
}

describe("codeOf", () => {
  it("is the BIC's institution code", () => {
    expect(codeOf(AURO)).toBe("AURO");
  });

  it("answers a short address whole rather than padding it", () => {
    expect(codeOf("X")).toBe("X");
  });
});

describe("layout", () => {
  const graph = layout({ institutions, wires });

  it("places every institution, admitted or not", () => {
    expect(graph.nodes.map((n) => n.bic).sort()).toEqual([AURO, CB, CSM, VERD].sort());
  });

  it("stacks the settlement agent above the clearing house above the banks", () => {
    const y = (bic: string) => graph.nodes.find((n) => n.bic === bic)!.y;
    expect(y(CB)).toBeLessThan(y(CSM));
    expect(y(CSM)).toBeLessThan(y(AURO));
  });

  it("draws no wire for a bank the scheme has not admitted", () => {
    expect(graph.edges.some((e) => e.subscriber === VERD)).toBe(false);
  });

  // The one wire that would otherwise run straight through the node it does not
  // end at. Everything else is a straight line.
  it("bows a bank's wire to the settlement agent and no other", () => {
    const bowed = graph.edges.find((e) => e.key === wireKey(AURO, CB))!;
    const straight = graph.edges.find((e) => e.key === wireKey(AURO, CSM))!;
    expect(bowed.path).toContain("Q");
    expect(straight.path).not.toContain("Q");
    expect(graph.edges.find((e) => e.key === wireKey(CSM, CB))!.path).not.toContain("Q");
  });

  // A dot placed by interpolating the two ends would sit off a curved wire,
  // which is the whole reason the bowed case is computed separately.
  it("rests a file ON a bowed wire rather than on the line between its ends", () => {
    const bowed = graph.edges.find((e) => e.key === wireKey(AURO, CB))!;
    const from = graph.nodes.find((n) => n.bic === AURO)!;
    const to = graph.nodes.find((n) => n.bic === CB)!;
    const chordX = from.x + (to.x - from.x) * 0.74;
    expect(Math.abs(bowed.restAt.towardHost.x - chordX)).toBeGreaterThan(5);
  });

  // A wire carries files both ways, so it has two places a file can wait, and
  // they must be at opposite ends rather than both near the host.
  it("rests the two directions at opposite ends of a wire", () => {
    const e = graph.edges.find((edge) => edge.key === wireKey(AURO, CSM))!;
    const from = graph.nodes.find((n) => n.bic === AURO)!;
    const toHost = Math.hypot(e.restAt.towardHost.x - from.x, e.restAt.towardHost.y - from.y);
    const toSub = Math.hypot(
      e.restAt.towardSubscriber.x - from.x,
      e.restAt.towardSubscriber.y - from.y,
    );
    expect(toHost).toBeGreaterThan(toSub);
  });

  // A bank in the middle of the row is wired straight through the clearing
  // house unless the bow is sized to clear it, and how many banks a deployment
  // has is what puts one there.
  it.each([3, 4, 5])("clears the clearing house with %i banks", (n) => {
    const many: Institution[] = [
      institutions[0],
      institutions[1],
      ...Array.from({ length: n }, (_, i) => ({
        bic: `BANK${i}XXXXXXX`,
        name: `Bank ${i}`,
        role: "member bank" as const,
      })),
    ];
    const g = layout({
      institutions: many,
      wires: many
        .filter((i) => i.role === "member bank")
        .map((i) => ({ subscriber: i.bic, host: CB })),
    });
    const csm = g.nodes.find((node) => node.bic === CSM)!;
    for (const e of g.edges) {
      const from = g.nodes.find((node) => node.bic === e.subscriber)!;
      const to = g.nodes.find((node) => node.bic === e.host)!;
      // A quadratic passes the clearing house's row halfway along, and sits at
      // (p0 + 2c + p1) / 4 there.
      const [, cx] = /Q (-?[\d.]+) /.exec(e.path)!;
      const midX = (from.x + 2 * Number(cx) + to.x) / 4;
      expect(Math.abs(midX - csm.x)).toBeGreaterThanOrEqual(30);
    }
  });

  it("keeps a bowed wire inside the canvas", () => {
    for (const e of graph.edges) {
      for (const p of [e.restAt.towardHost, e.restAt.towardSubscriber]) {
        expect(p.x).toBeGreaterThanOrEqual(0);
        expect(p.x).toBeLessThanOrEqual(CANVAS.width);
      }
    }
  });

  it("puts a lone bank in the middle rather than at the left margin", () => {
    const one = layout({
      institutions: institutions.filter((i) => i.bic !== VERD),
      wires: [],
    });
    expect(one.nodes.find((n) => n.bic === AURO)!.x).toBe(CANVAS.width / 2);
  });

  it("drops a wire naming an institution the mesh did not list", () => {
    const g = layout({ institutions, wires: [{ subscriber: "GHOSTXXXXXX", host: CB }] });
    expect(g.edges).toHaveLength(0);
  });
});

describe("isResting", () => {
  it("is a file one end sent and the other has not taken", () => {
    expect(isResting(crossing(CSM, AURO, false))).toBe(true);
    expect(isResting(crossing(CSM, AURO, true))).toBe(false);
  });

  // A crossing the sender holds no record of is a missing record, not a stage a
  // file passes through, so it must not be drawn as one waiting to be collected.
  it("is not a take with no send", () => {
    const orphan = { ...crossing(CSM, AURO, true), sentAt: undefined };
    expect(isResting(orphan)).toBe(false);
  });
});

describe("restingByWire", () => {
  // Which end dialled is not a property of the file: a bank uploads to its
  // clearing house and collects from it over the one wire.
  it("puts both directions of travel on the same wire", () => {
    const out = restingByWire(
      [crossing(AURO, CSM, false), crossing(CSM, AURO, false)],
      wires,
    );
    expect([...out.keys()]).toEqual([wireKey(AURO, CSM)]);
  });

  // A file waits at the end it is TRAVELLING to, and a wire carries files both
  // ways — so an uploaded file and one waiting to be collected are two dots at
  // opposite ends of one line, not one dot wherever the dialler happens to be.
  it("separates the two directions of travel", () => {
    const out = restingByWire(
      [crossing(AURO, CSM, false), crossing(CSM, AURO, false), crossing(CSM, AURO, false)],
      wires,
    ).get(wireKey(AURO, CSM))!;
    expect(out.towardHost).toHaveLength(1);
    expect(out.towardSubscriber).toHaveLength(2);
  });

  it("holds no delivered file", () => {
    expect(restingByWire([crossing(AURO, CSM, true)], wires).size).toBe(0);
  });

  it("drops a crossing between two ends that share no wire", () => {
    expect(restingByWire([crossing(VERD, AURO, false)], wires).size).toBe(0);
  });
});

describe("forReading", () => {
  function onDay(from: string, day: string, took: boolean): Crossing {
    return {
      ...crossing(from, CSM, took),
      msgId: `${from}-${day}`,
      sentAt: `${day}T09:00:00Z`,
      receivedAt: took ? `${day}T09:00:00Z` : undefined,
    };
  }

  it("puts what is still in flight first", () => {
    const out = forReading([onDay(AURO, "2025-09-15", true), onDay(VERD, "2025-09-15", false)]);
    expect(out[0].msgId).toBe(`${VERD}-2025-09-15`);
  });

  it("puts the most recent day first", () => {
    const out = forReading([onDay(AURO, "2025-09-15", true), onDay(VERD, "2025-09-16", true)]);
    expect(out.map((c) => c.msgId)).toEqual([
      `${VERD}-2025-09-16`,
      `${AURO}-2025-09-15`,
    ]);
  });

  // Within one day there is no chronology to impose, so the mesh's own grouping
  // — a sender's traffic in the order that sender sent it — is left alone.
  it("keeps the mesh's order within a day", () => {
    const a = onDay(AURO, "2025-09-15", true);
    const b = { ...onDay(VERD, "2025-09-15", true), msgId: "second" };
    const c = { ...onDay(VERD, "2025-09-15", true), msgId: "third" };
    expect(forReading([a, b, c]).map((x) => x.msgId)).toEqual([a.msgId, "second", "third"]);
  });
});
