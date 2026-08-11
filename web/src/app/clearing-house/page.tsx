"use client";

import Link from "next/link";

import { PageHeader } from "@/components/page-header";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { IdText } from "@/components/id-text";
import { Hint } from "@/components/hint";
import type { HintKey } from "@/components/hint-content";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { useCycles, useIsProvisioned, useParticipants, usePayments, useSettlements } from "@/lib/api/hooks";
import { backendFor, homeFor } from "@/lib/identity";
import type { Participant } from "@/lib/types";

// A payment is "in flight" until it reaches a terminal state. Settled, Rejected
// and Returned payments are done; everything before that is still moving.
const IN_FLIGHT = new Set(["Initiated", "Accepted", "Cleared"]);

// No reserve figures here. Reading `useReserves()` → the central bank's
// `GET /reserves` would make this the only screen in the app reading a listener
// whose whole point is that it belongs to someone else — the lobby's own copy
// says the clearing house sees payments and the central bank sees reserves. This
// page keeps membership, payments, cycles, settlements, schemes and directory;
// reserves live at /central-bank.
export default function ClearingHouse() {
  const { data: participants, isLoading, error, refetch } = useParticipants();
  const { data: cycles } = useCycles();
  const { data: payments } = usePayments();
  const { data: settlements } = useSettlements();
  const isProvisioned = useIsProvisioned();

  // Members, not banks. Provisioning writes three rows at three institutions and
  // commits four times, so a bank can be in this list before the scheme has
  // answered it: the card below says which, and a stat labelled "Member banks"
  // that counted those too would be the one number on this page that was not
  // true.
  const members = (participants ?? []).filter(isAdmitted).length;
  const openCycles = (cycles ?? []).filter((c) => c.status === "Open").length;
  const inFlight = (payments ?? []).filter((p) => IN_FLIGHT.has(p.status)).length;
  const settlementCount = (settlements ?? []).length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Clearing house"
        hint="clearing-vs-settlement"
        description="An interbank payment network running on a double-entry ledger. The banks it clears for meet at the central bank to settle. A bank the scheme has not answered is listed here as well, and is not one of them yet."
      />

      <HowMoneyMoves />

      {/* Network at a glance — degrades to zeros while the lists load. */}
      <div className="grid gap-4 grid-cols-2 lg:grid-cols-4">
        <Stat label="Member banks">{members}</Stat>
        <Stat label="Open cycles" hint="netting">
          {openCycles}
        </Stat>
        <Stat label="In-flight payments" hint="payment-lifecycle">
          {inFlight}
        </Stat>
        <Stat label="Settlements" hint="settlement-model-net">
          {settlementCount}
        </Stat>
      </div>

      <section className="space-y-3">
        {/* Banks, not members. The STAT above counts members and this list does
            not filter: a bank the scheme has not answered belongs on the console
            the network's membership is watched from, and its card says so. */}
        <h2 className="text-sm font-medium text-muted-foreground">Banks</h2>
        {error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : isLoading ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <Skeleton className="h-28" />
            <Skeleton className="h-28" />
            <Skeleton className="h-28" />
          </div>
        ) : participants && participants.length === 0 ? (
          <Card>
            <CardContent className="py-10 text-sm text-muted-foreground">
              {/* No "create participant" button, and no route behind one at any
                  listener. Which banks a deployment has is the deployment's
                  decision, made before the process starts; what this institution
                  does about one is write the routing entry it clears through. */}
              No banks yet. Which banks this network has is settled when the
              deployment is provisioned, not from a console; this list is where
              they are seen becoming members.
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {participants?.map((p) => (
              <ParticipantCard
                key={p.id}
                participant={p}
                provisioned={isProvisioned(backendFor({ persona: "bank", pid: p.id }))}
              />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

// One bank's card: name, id, and where it has got to. No reserve figure — see
// the file-level comment above. Banks with no listener are shown, not linked:
// entering one would mean a console whose every request 502s, the same rule the
// lobby and the identity picker already follow.
//
// A bank the scheme has not answered says so instead of claiming membership — no
// settlement account, no routing entry — and that is an ordinary state rather
// than a broken one, because provisioning commits four times at three
// institutions and this list is read from one of them. "Member of the network"
// is not true of every bank in it.
function ParticipantCard({
  participant: p,
  provisioned,
}: {
  participant: Participant;
  provisioned: boolean;
}) {
  const body = (
    <Card
      className={
        provisioned ? "h-full transition-colors hover:border-foreground/30" : "h-full opacity-70"
      }
    >
      <CardHeader>
        <CardTitle className="text-base">{p.name}</CardTitle>
        <IdText id={p.id} />
      </CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground">
          {!isAdmitted(p)
            ? "The scheme has not answered this bank: it can open customer accounts but not fund them, and a payment to or from it is refused until the answer arrives."
            : provisioned
              ? "Member of the network."
              : "Awaiting provisioning."}
        </p>
      </CardContent>
    </Card>
  );
  return provisioned ? <Link href={homeFor({ persona: "bank", pid: p.id })}>{body}</Link> : body;
}

// A bank the scheme has answered: its own row names the settlement account the
// agent opened for it. Nothing on the wire says so more directly — there is no
// status field, and an empty settlement reference is what says the answer has
// not arrived. See ParticipantAccounts.settlement.
//
// `some` and not `every`: one application asks for one currency, so a bank in two
// assets is answered twice and one answered in one of them is a member of the
// network in that one.
function isAdmitted(p: Participant): boolean {
  return p.assets.some((a) => a.settlement !== "");
}

// Compact single-metric card for the "at a glance" row.
function Stat({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: HintKey;
  children: React.ReactNode;
}) {
  return (
    <Card size="sm">
      <CardContent>
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          {label}
          {hint && <Hint id={hint} />}
        </p>
        <div className="mt-1 text-2xl font-semibold tabular-nums">{children}</div>
      </CardContent>
    </Card>
  );
}

// The teaching centrepiece: the five-step life of money through the system,
// each step linking to the concept that explains it. Visible even with no data
// so a first-time visitor knows what to do.
const STEPS: { title: string; body: string; hint: HintKey }[] = [
  {
    // "Join" and not "Create", because nothing here creates a bank: which banks
    // the network has is settled when it is set up. What this step is about is
    // the three institutions a bank's arrival takes, which is the part a reader
    // of the other four steps needs.
    title: "Join",
    body: "A bank founds itself, the central bank opens its settlement account, and the clearing house writes where to route to it.",
    hint: "double-entry",
  },
  {
    title: "Fund",
    body: "Credit a deposit account; the bank's central-bank reserve rises in step.",
    hint: "reserve-account",
  },
  {
    title: "Pay",
    body: "A payment is initiated from one bank's customer to another's.",
    hint: "payment-lifecycle",
  },
  {
    title: "Clear",
    body: "Payments are grouped into a cycle and netted to a single figure per bank.",
    hint: "netting",
  },
  {
    title: "Settle",
    body: "Reserves move at the central bank by each bank's net position.",
    hint: "clearing-vs-settlement",
  },
];

function HowMoneyMoves() {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">How money moves</CardTitle>
      </CardHeader>
      <CardContent>
        <ol className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
          {STEPS.map((step, i) => (
            <li key={step.title} className="flex gap-3 lg:flex-col lg:gap-2">
              <span
                aria-hidden
                className="flex size-7 shrink-0 items-center justify-center rounded-full bg-accent text-sm font-semibold text-accent-foreground"
              >
                {i + 1}
              </span>
              <div className="space-y-1">
                <p className="flex items-center gap-1.5 text-sm font-medium">
                  {step.title}
                  <Hint id={step.hint} />
                </p>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {step.body}
                </p>
              </div>
            </li>
          ))}
        </ol>
      </CardContent>
    </Card>
  );
}
