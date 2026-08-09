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
// is not domain data. A bank with no listener is still a bank, and the list this
// probes is every bank GET /members returns — founded ones included, which is
// why the two states have to be told apart here rather than by a 502.
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
  // The bank list is GET /members on the CENTRAL BANK's listener, beside the
  // route that founds them: it is the operator's read of a deployment rather
  // than an institution's own record, and the clearing house — which holds a
  // roster and no banks table — cannot answer it. Reading it doubles as that
  // listener's probe, so the central bank gets no separate request.
  let banksListed: string[] = [];
  let centralBankLive = false;
  try {
    const res = await fetch(`${institutionUrl("central-bank", CFG)}/members`, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    centralBankLive = res.ok;
    if (res.ok) banksListed = ((await res.json()) as { id: string }[]).map((m) => m.id);
  } catch {
    // Leave the list empty. With the central bank down there is no list of banks
    // to read, and reporting every bank as dead would be a guess rather than an
    // answer — the caller sees central-bank:false and knows why the list is
    // short.
  }

  // Every probe below is independent — none needs another's answer — so they
  // run in one Promise.all rather than the clearing house's waiting its turn
  // behind the banks': serialized, that's up to one more probe timeout added
  // to every load of this route for no reason.
  const [csmLive, banks] = await Promise.all([
    probe(institutionUrl("clearing-house", CFG)),
    Promise.all(
      banksListed.map(async (pid): Promise<OperatorStatus> => {
        // pid always comes from this same list, so its position is always found
        // and bankUrl never throws here — the port is always derivable. What is
        // not guaranteed is that anything is bound to it: a bank founded at
        // runtime gets a derived port and no listener until the server restarts,
        // so probe() failing against that port is what tells a bank on the list
        // from one with a console, not a thrown error.
        return { operator: `bank/${pid}`, live: await probe(bankUrl(pid, banksListed, CFG)) };
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
