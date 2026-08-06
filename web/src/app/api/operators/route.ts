import { NextResponse } from "next/server";

import {
  backendConfig,
  bankUrl,
  institutionUrl,
  type OperatorStatus,
} from "@/lib/api/backend-url";

// Which operators actually have a listener behind them.
//
// Ports are static by design: a bank founded at runtime through POST /members
// gets a store row and a chart of accounts of its own and no listener until the
// server restarts, because joining a network is an operational act and modelling
// it as an API call that instantly yields a running bank teaches the wrong
// thing — the settlement account it needs before it can settle anything is
// another institution's to open, after that call has already answered. The
// lobby and the identity picker need to tell those two states apart *before*
// offering a console, so this answers once for every bank rather than letting
// each one discover it through a 502.
//
// This is Next's own knowledge and is served by no backend: deployment topology
// is not domain data. A member of the roster with no listener is still a member.
export const dynamic = "force-dynamic";

const CFG = backendConfig(process.env);
const PROBE_TIMEOUT_MS = 1_500;

// A listener is live if it answers at all. GET /assets is the probe because
// every operator serves it — it is a compiled-in constant, which is exactly why
// it is on all three surfaces — so one request shape works for every key.
async function probe(base: string): Promise<boolean> {
  try {
    const res = await fetch(`${base}/assets`, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return res.ok;
  } catch {
    return false;
  }
}

export async function GET() {
  const csm = institutionUrl("clearing-house", CFG);

  let roster: string[] = [];
  let csmLive = false;
  try {
    const res = await fetch(`${csm}/members`, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    csmLive = res.ok;
    if (res.ok) roster = ((await res.json()) as { id: string }[]).map((m) => m.id);
  } catch {
    // Leave the roster empty. With the clearing house down there is no roster to
    // read, and reporting every bank as dead would be a guess rather than an
    // answer — the caller sees clearing-house:false and knows why the list is
    // short.
  }

  // Every probe below is independent — none needs another's answer — so they
  // run in one Promise.all rather than the central bank's waiting its turn
  // behind the banks': serialized, that's up to one more probe timeout added
  // to every load of this route for no reason.
  const [centralBankLive, banks] = await Promise.all([
    probe(institutionUrl("central-bank", CFG)),
    Promise.all(
      roster.map(async (pid): Promise<OperatorStatus> => {
        // pid always comes from this same roster, so its position is always
        // found and bankUrl never throws here — the port is always derivable.
        // What is not guaranteed is that anything is bound to it: a bank
        // founded at runtime gets a derived port and no listener until the
        // server restarts, so probe() failing against that port is what tells a
        // bank on the list from one with a console, not a thrown error.
        return { operator: `bank/${pid}`, live: await probe(bankUrl(pid, roster, CFG)) };
      }),
    ),
  ]);

  const out: OperatorStatus[] = [
    { operator: "central-bank", live: centralBankLive },
    { operator: "clearing-house", live: csmLive },
    ...banks,
  ];
  return NextResponse.json(out);
}
