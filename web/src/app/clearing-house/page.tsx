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

// This screen used to also show a "Total reserves" stat and a per-bank
// reserve figure, both read from `useReserves()` → the central bank's
// `GET /reserves`. That made it the only screen in the app reading a listener
// whose whole point is that it belongs to someone else — the lobby's own copy
// says the clearing house sees payments and the central bank sees reserves.
// Removing them is the point of the operator split, not a gap: this page
// keeps membership, payments, cycles, settlements, schemes and directory, and
// reserves live at /central-bank now.
export default function ClearingHouse() {
  const { data: participants, isLoading, error, refetch } = useParticipants();
  const { data: cycles } = useCycles();
  const { data: payments } = usePayments();
  const { data: settlements } = useSettlements();
  const isProvisioned = useIsProvisioned();

  // Members, not banks. Admission is a conversation, so a bank can be in this
  // list and not be a member of anything yet: the card below says which, and a
  // stat labelled "Member banks" that counted applicants too would be the one
  // number on this page that was not true.
  const members = (participants ?? []).filter((p) => p.status === "Member").length;
  const openCycles = (cycles ?? []).filter((c) => c.status === "Open").length;
  const inFlight = (payments ?? []).filter((p) => IN_FLIGHT.has(p.status)).length;
  const settlementCount = (settlements ?? []).length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Clearing house"
        hint="clearing-vs-settlement"
        description="An interbank payment network running on a double-entry ledger. Each participant is a member bank; they meet at the central bank to settle."
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
        <h2 className="text-sm font-medium text-muted-foreground">
          Member banks
        </h2>
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
              {/* Admission is the central bank's act: opening a member's
                  reserve and settlement accounts happens in its own book, so
                  there is no "create participant" button here — see
                  /central-bank. */}
              No participants yet. A member bank is admitted at the central
              bank, not here.
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
// the file-level comment above. Unprovisioned banks are shown, not linked:
// entering one would mean a console whose every request 502s, the same rule the
// lobby and the identity picker already follow.
//
// A FOUNDED bank says so instead of claiming membership. It is a bank whose
// application the scheme has not answered — no settlement account, no routing
// entry — and it is an ordinary state rather than a broken one, because
// admission is a conversation between three institutions and this list is read
// from one of them. The card used to say "Member of the network" for every bank
// in it, which was true only while founding and joining were one commit.
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
          {p.status !== "Member"
            ? "Founded. The scheme has not answered its application: it can open customer accounts but not fund them, and no cut-off it takes part in can settle."
            : provisioned
              ? "Member of the network."
              : "Awaiting provisioning."}
        </p>
      </CardContent>
    </Card>
  );
  return provisioned ? <Link href={homeFor({ persona: "bank", pid: p.id })}>{body}</Link> : body;
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
    // "Create" and not "Join", because founding and joining are two steps now
    // and this one is the first: a bank is created, and what it does next is
    // apply. The body used to say a member bank joins the network, which is the
    // step after this one and is answered by two institutions that are not the
    // bank.
    title: "Create",
    body: "A bank is founded and applies to the scheme; the central bank and the clearing house answer.",
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
