// The mesh as geometry: where each institution sits, what path each wire takes,
// and which files are resting on which of them.
//
// It is here rather than inside the drawing because none of it is React, and
// because there is no component runner in this repo — a rule about where a file
// rests is only pinned by a test if it can be reached without rendering.

import type { Crossing, Institution, InstitutionRole, NetworkFlow, Wire } from "./types";

// The drawing's own coordinate space, rendered through a viewBox so one layout
// serves the rail and the lobby alike.
export const CANVAS = { width: 320, height: 200 };

const HOST_ROW = { settlement: 26, clearing: 92 };
const BANK_ROW = 168;
// How far from the edge the outermost bank sits, so its label has room.
const MARGIN = 34;

export interface Point {
  x: number;
  y: number;
}

export interface Node {
  bic: string;
  name: string;
  role: InstitutionRole;
  x: number;
  y: number;
  // The BIC's institution code — the first four characters, which is what a
  // banker reads a BIC's owner off. Short enough to sit under a node.
  code: string;
}

export interface Edge {
  key: string;
  subscriber: string;
  host: string;
  // An SVG path. A bank reaches its clearing house in a straight line and its
  // settlement agent around the outside of one, because that wire has to pass
  // the node it does not end at.
  path: string;
  // Where a file rests when nobody has collected it: near the end it is
  // travelling TOWARDS. One wire carries files both ways — a bank uploads to
  // its clearing house over the same wire it collects from — so which end a dot
  // sits at is the file's own direction and never who dialled.
  restAt: { towardHost: Point; towardSubscriber: Point };
  // True when this wire carries the reserve conversation rather than the
  // payment path, which is a distinction the drawing makes because the two
  // teach different halves of how money moves.
  toSettlementAgent: boolean;
}

export interface Graph {
  nodes: Node[];
  edges: Edge[];
}

// Which way along a wire the files resting on it are travelling. One wire
// carries both: a bank uploads to its clearing house and collects from it over
// the same connection it dialled.
export interface Resting {
  towardHost: Crossing[];
  towardSubscriber: Crossing[];
}

// wireKey names a wire by its two ends, in the order the parts are played. A
// crossing goes both ways along one wire, so a file's own direction is not part
// of it.
export function wireKey(subscriber: string, host: string): string {
  return `${subscriber}|${host}`;
}

// codeOf is the institution code of a BIC: characters 1-4. A BIC shorter than
// that is answered whole rather than padded, because a deployment configured
// with a stub address should still draw.
export function codeOf(bic: string): string {
  return bic.slice(0, 4) || bic;
}

// layout places every institution and routes every wire. Banks are spread along
// the bottom in the order the deployment lists them, which is the order their
// listeners were bound: stable across a reload, so a node does not move because
// a file did.
export function layout(flow: Pick<NetworkFlow, "institutions" | "wires">): Graph {
  const at = new Map<string, Node>();
  const banks = flow.institutions.filter((i) => i.role === "member bank");

  for (const i of flow.institutions) {
    at.set(i.bic, { ...i, code: codeOf(i.bic), ...place(i, banks) });
  }

  const nodes = flow.institutions.map((i) => at.get(i.bic)!);
  const edges: Edge[] = [];
  for (const w of flow.wires) {
    const edge = route(w, at);
    if (edge) edges.push(edge);
  }
  return { nodes, edges };
}

function place(i: Institution, banks: Institution[]): { x: number; y: number } {
  if (i.role === "settlement agent") return { x: CANVAS.width / 2, y: HOST_ROW.settlement };
  if (i.role === "clearing house") return { x: CANVAS.width / 2, y: HOST_ROW.clearing };
  const index = banks.findIndex((b) => b.bic === i.bic);
  return { x: spread(index, banks.length), y: BANK_ROW };
}

// spread puts n banks along the bottom edge. One bank sits in the middle rather
// than at the left margin, which is what an even division would do to it.
function spread(index: number, n: number): number {
  if (n <= 1) return CANVAS.width / 2;
  const usable = CANVAS.width - 2 * MARGIN;
  return MARGIN + (usable * index) / (n - 1);
}

function route(w: Wire, at: Map<string, Node>): Edge | null {
  const from = at.get(w.subscriber);
  const to = at.get(w.host);
  // A wire naming an institution the mesh did not list is dropped rather than
  // drawn to nowhere. It should not happen; a half-drawn graph is worse if it
  // does.
  if (!from || !to) return null;

  const toSettlementAgent = to.role === "settlement agent";
  // A bank's wire to the settlement agent is the only one that would otherwise
  // run through the clearing house's node, so it is the only one bowed.
  const bowed = toSettlementAgent && from.role === "member bank";
  const path = bowed ? bow(from, to) : `M ${from.x} ${from.y} L ${to.x} ${to.y}`;
  const pointAt = (t: number) => (bowed ? bowPoint(from, to, t) : along(from, to, t));

  return {
    key: wireKey(w.subscriber, w.host),
    subscriber: w.subscriber,
    host: w.host,
    path,
    restAt: { towardHost: pointAt(REST_ALONG), towardSubscriber: pointAt(1 - REST_ALONG) },
    toSettlementAgent,
  };
}

// How far along a wire a resting file is drawn, as a fraction from the end it
// left. Near the far end, because a file resting is one the party there has not
// come for.
const REST_ALONG = 0.74;

// How far a bowed wire is pushed away from the centre line, and how much room
// it has to leave around the node it is passing. CLEARANCE is measured from
// that node's centre, so it holds whatever the node's own width turns out to be.
const BOW = 46;
const CLEARANCE = 36;

// control is the bend in a bank's wire to the settlement agent. The bend is
// outward from the centre — leftward for a bank sitting exactly on it — and it
// is at least enough to clear the clearing house, which a bank near the middle
// of the row would otherwise be wired straight through.
function control(from: Node, to: Node): { x: number; y: number } {
  const centre = CANVAS.width / 2;
  const left = from.x <= centre;
  // A quadratic sits at (p0 + 2c + p1) / 4 halfway along, and halfway is where
  // this wire passes the clearing house. Solving that for c is what "at least
  // enough to clear it" means; the minimum bow is what an outer bank gets, which
  // needs none.
  const clear = (4 * (centre + (left ? -CLEARANCE : CLEARANCE)) - from.x - to.x) / 2;
  const x = left ? Math.min(clear, from.x - BOW) : Math.max(clear, from.x + BOW);
  return { x: clamp(x, -40, CANVAS.width + 40), y: (from.y + to.y) / 2 };
}

function bow(from: Node, to: Node): string {
  const c = control(from, to);
  return `M ${from.x} ${from.y} Q ${c.x} ${c.y} ${to.x} ${to.y}`;
}

// bowPoint is the quadratic Bézier at t, which is where a dot has to sit to be
// ON the curve rather than on the straight line between its ends.
function bowPoint(from: Node, to: Node, t: number): { x: number; y: number } {
  const c = control(from, to);
  const u = 1 - t;
  return {
    x: u * u * from.x + 2 * u * t * c.x + t * t * to.x,
    y: u * u * from.y + 2 * u * t * c.y + t * t * to.y,
  };
}

function along(from: Node, to: Node, t: number): { x: number; y: number } {
  return { x: from.x + (to.x - from.x) * t, y: from.y + (to.y - from.y) * t };
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v));
}

// isResting is the one state this drawing exists to show: a file one end sent
// and the other has not taken. A crossing with no SEND is not resting — it is a
// record the sender is missing, which is a defect rather than a stage.
export function isResting(c: Crossing): boolean {
  return c.sentAt !== undefined && c.receivedAt === undefined;
}

// restingByWire groups the files still on a wire by the wire they are on.
//
// A crossing names a sender and a recipient; a wire names a subscriber and a
// host. Which of the two ends dialled is not a property of the file, so the
// pair is matched in either orientation and a crossing between two ends that
// share no wire is dropped — a bank the scheme has not admitted holds files and
// is reachable by nobody.
export function restingByWire(crossings: Crossing[], wires: Wire[]): Map<string, Resting> {
  const known = new Map<string, Wire>();
  for (const w of wires) known.set(pair(w.subscriber, w.host), w);

  const out = new Map<string, Resting>();
  for (const c of crossings) {
    if (!isResting(c)) continue;
    const w = known.get(pair(c.from, c.to));
    if (!w) continue;
    const key = wireKey(w.subscriber, w.host);
    let held = out.get(key);
    if (!held) out.set(key, (held = { towardHost: [], towardSubscriber: [] }));
    if (c.to === w.host) held.towardHost.push(c);
    else held.towardSubscriber.push(c);
  }
  return out;
}

// forReading is the order a reader meets the crossings in, and it exists
// because the order the mesh arrives in is not one.
//
// A deployment's clock does not move within a day, so every file that crossed
// on one day carries the SAME instant and there is no chronology inside it to
// sort by. The mesh breaks that tie with the sender's address and its own seq,
// which groups a sender's traffic in the order that sender sent it, and that
// grouping is kept. What is imposed over it is the two things a reader is
// after: what is still in flight, and the most recent day first. Reversing the
// mesh would have read as a timeline and been alphabetical by sender.
export function forReading(crossings: Crossing[]): Crossing[] {
  const resting: Crossing[] = [];
  const byDay = new Map<string, Crossing[]>();
  for (const c of crossings) {
    if (isResting(c)) {
      resting.push(c);
      continue;
    }
    const day = (c.receivedAt ?? c.sentAt ?? "").slice(0, 10);
    const held = byDay.get(day);
    if (held) held.push(c);
    else byDay.set(day, [c]);
  }
  const newestFirst = [...byDay.keys()].sort().reverse();
  return [...resting, ...newestFirst.flatMap((d) => byDay.get(d)!)];
}

function pair(a: string, b: string): string {
  return a < b ? `${a} ${b}` : `${b} ${a}`;
}
