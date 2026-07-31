"use client";

import Link from "next/link";

import { PageHeader } from "@/components/page-header";
import { CreateParticipantDialog } from "@/components/create-participant-dialog";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Money, UnresolvedAmount } from "@/components/money";
import { IdText } from "@/components/id-text";
import { Hint } from "@/components/hint";
import type { HintKey } from "@/components/hint-content";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAssetLookup,
  useCycles,
  useParticipants,
  usePayments,
  useReserves,
  useSettlements,
} from "@/lib/api/hooks";
import type { Participant, Reserve } from "@/lib/types";

// A payment is "in flight" until it reaches a terminal state. Settled, Rejected
// and Returned payments are done; everything before that is still moving.
const IN_FLIGHT = new Set(["Initiated", "Accepted", "Cleared"]);

export default function Home() {
  const { data: participants, isLoading, error, refetch } = useParticipants();
  const { data: reserves, isLoading: reservesLoading } = useReserves();
  const { data: cycles } = useCycles();
  const { data: payments } = usePayments();
  const { data: settlements } = useSettlements();

  // A bank holds one reserve account per asset, so both of these are per
  // asset. Reserves in different assets are different things: picking whichever
  // row came first, or adding them up, would state a number that is not true of
  // anything.
  const reservesFor = (pid: string) =>
    (reserves ?? []).filter((r) => r.participant === pid);

  // Sum per asset code. A code means the same asset everywhere — the
  // definitions are compiled in, not registered per book — so the codes are
  // the only key the sum needs.
  const totalsByAsset = (reserves ?? []).reduce<Record<string, number>>(
    (totals, r) => ({ ...totals, [r.asset]: (totals[r.asset] ?? 0) + r.reserve }),
    {},
  );
  const assetTotals = Object.entries(totalsByAsset).sort(([a], [b]) =>
    a.localeCompare(b),
  );
  const openCycles = (cycles ?? []).filter((c) => c.status === "Open").length;
  const inFlight = (payments ?? []).filter((p) => IN_FLIGHT.has(p.status)).length;
  const settlementCount = (settlements ?? []).length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Dashboard"
        hint="clearing-vs-settlement"
        description="An interbank payment network running on a double-entry ledger. Each participant is a member bank; they meet at the central bank to settle."
        actions={<CreateParticipantDialog />}
      />

      <HowMoneyMoves />

      {/* Network at a glance — degrades to zeros while the lists load. */}
      <div className="grid gap-4 grid-cols-2 lg:grid-cols-5">
        <Stat label="Member banks">{participants?.length ?? 0}</Stat>
        <Stat label="Total reserves" hint="central-bank-reserves">
          {assetTotals.length === 0 ? (
            reservesLoading ? (
              <Skeleton className="h-6 w-20" />
            ) : (
              <span className="text-sm text-muted-foreground">None yet.</span>
            )
          ) : (
            assetTotals.map(([code, total]) => (
              <AssetTotalRow key={code} code={code} total={total} />
            ))
          )}
        </Stat>
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
            <CardContent className="flex flex-col items-start gap-3 py-10">
              <p className="text-sm text-muted-foreground">
                No participants yet. Create your first member bank to start
                moving money.
              </p>
              <CreateParticipantDialog />
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {participants?.map((p) => (
              <ParticipantCard key={p.id} participant={p} reserves={reservesFor(p.id)} />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

// One asset code's reserve total, summed across every participant. Summing is
// only meaningful within an asset, which is why there is a row per code rather
// than one number.
function AssetTotalRow({
  code,
  total,
}: {
  code: string;
  total: number;
}) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(code);
  if (!asset) {
    return <UnresolvedAmount code={code} isLoading={isLoading} className="block" />;
  }
  return (
    <span className="block">
      <Money amount={total} asset={asset} />
    </span>
  );
}

// One member-bank card: name, id, and one reserve line per asset it holds.
function ParticipantCard({
  participant: p,
  reserves,
}: {
  participant: Participant;
  reserves: Reserve[];
}) {
  const { byCode, isLoading } = useAssetLookup();
  return (
    <Link href={`/bank/${p.id}`}>
      <Card className="h-full transition-colors hover:border-foreground/30">
        <CardHeader>
          <CardTitle className="text-base">{p.name}</CardTitle>
          <IdText id={p.id} />
        </CardHeader>
        <CardContent>
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            Reserves
            <Hint id="central-bank-reserves" />
          </p>
          {reserves.length === 0 ? (
            isLoading ? (
              <Skeleton className="h-6 w-20" />
            ) : (
              <p className="text-sm text-muted-foreground">None yet.</p>
            )
          ) : (
            reserves.map((r) => {
              const asset = byCode.get(r.asset);
              return asset ? (
                <p key={r.asset} className="text-lg font-semibold">
                  <Money amount={r.reserve} asset={asset} />
                </p>
              ) : (
                <Skeleton key={r.asset} className="h-6 w-20" />
              );
            })
          )}
        </CardContent>
      </Card>
    </Link>
  );
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
    title: "Create",
    body: "A member bank joins the network.",
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
