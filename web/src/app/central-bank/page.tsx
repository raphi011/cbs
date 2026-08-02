"use client";

import { PageHeader } from "@/components/page-header";
import { CreateParticipantDialog } from "@/components/create-participant-dialog";
import { DataTable, type Column } from "@/components/data-table";
import { AmountCell, Money, UnresolvedAmount } from "@/components/money";
import { IdText } from "@/components/id-text";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NetPositionsTable } from "@/components/net-positions-table";
import {
  useAssetLookup,
  useCentralBankCycles,
  useCentralBankSettlements,
  useReserves,
} from "@/lib/api/hooks";
import type { ClearingCycle, Reserve, Settlement } from "@/lib/types";

// A reserve's amount can only be rendered once its asset's scale is known.
// Reserves in different assets are different things, so each row resolves its
// own code rather than the page assuming one.
function ReserveAmountCell({ reserve }: { reserve: Reserve }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(reserve.asset);
  if (!asset) {
    return (
      <UnresolvedAmount
        code={reserve.asset}
        isLoading={isLoading}
        className="ml-auto block text-right"
      />
    );
  }
  return <AmountCell amount={reserve.reserve} asset={asset} />;
}

const reserveColumns: Column<Reserve>[] = [
  { key: "participant", header: "Participant", render: (r) => <IdText id={r.participant} /> },
  { key: "asset", header: "Asset", render: (r) => r.asset },
  {
    key: "reserve",
    header: "Reserve",
    align: "right",
    hint: "reserve-account",
    render: (r) => <ReserveAmountCell reserve={r} />,
  },
];

// Shortfall is why an instruction was refused: which bank could not cover, what
// it owed and what it held.
interface Shortfall {
  participant: string;
  owed: number;
  held: number;
}

// The settlement layer. Banks meet only here, and what the central bank does is
// move reserves between them — it never sees an individual payment, which is
// what the operator split made expressible for the first time.
//
// Admission is the central bank's act too: opening a member's reserve and
// settlement accounts happens in the central bank's own book, which is why
// POST /members is its route and not the clearing house's.
//
// # This console no longer settles anything
//
// It used to carry a Settle button per closed cycle. Settlement is now performed
// on INSTRUCTION — the clearing house reaches a cut-off and sends a pacs.009,
// and the central bank's actor answers it — so there is no act left for a human
// here and no route behind one. What is left is watching, and the three sections
// below are what watching consists of: the instructions still outstanding, the
// ones that were discharged, and the reserves they moved. A cycle sitting in the
// first section still Closed is one the central bank REFUSED, and the reserves
// are why.
export default function CentralBankPage() {
  const reserves = useReserves();
  const cycles = useCentralBankCycles();
  const settlements = useCentralBankSettlements();

  return (
    <div className="space-y-8">
      <PageHeader
        title="Central bank"
        hint="central-bank-reserves"
        description="Banks meet only here. The central bank holds one reserve account per participant and asset, and settlement is reserves moving between them."
        actions={<CreateParticipantDialog />}
      />

      <SettlementInstructions
        cycles={(cycles.data ?? []).filter((c) => c.status === "Closed")}
        reserves={reserves.data ?? []}
        isLoading={cycles.isLoading}
        error={cycles.error}
        onRetry={() => cycles.refetch()}
      />

      <Settlements
        settlements={settlements.data ?? []}
        isLoading={settlements.isLoading}
        error={settlements.error}
        onRetry={() => settlements.refetch()}
      />

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Reserves</h2>
        {reserves.error ? (
          <ErrorState error={reserves.error} onRetry={() => reserves.refetch()} />
        ) : (
          <DataTable
            columns={reserveColumns}
            rows={reserves.data}
            rowKey={(r) => `${r.participant}:${r.asset}`}
            isLoading={reserves.isLoading}
            empty="No participants yet. Admit one to see its reserve account."
          />
        )}
      </section>
    </div>
  );
}

// shortfallOf is a net payer this cycle cannot settle for, or null.
//
// It is COMPUTED from two things the console already holds — the cycle's net
// positions and the reserves table — and not read from anywhere, because there
// is nowhere to read it from. The central bank's refusal travels back to the
// clearing house as a pacs.002 carrying RJCT/AM04; nothing about it is stored,
// and the only trace it leaves is the cycle staying Closed with no settlement
// against it. So the console reconstructs the reason the way an operator would:
// which bank owes more than it holds.
//
// It answers ONE such bank and not all of them, and the alert says "a net payer"
// rather than "the net payer" for a reason worth being exact about. Settlement
// is one unit of work over the whole batch, so the first payer the settlement
// agent reaches that cannot cover aborts it and the rest are never posted — a
// second underfunded member is not a second failure, it is the same one waiting
// behind it. But WHICH one that was is not something this console can know:
// SettleCycleTx visits members in REGISTRATION order (payment/system.go's
// settlementLegsTx, which says so and says why), and nothing here is in
// registration order — netPositions arrives as a JSON object, whose keys Go's
// encoder sorts lexically, and `bank_80` sorts before `bank_9`.
//
// Naming a bank that genuinely cannot cover is true, and it is what the operator
// has to act on. Naming it as the one that aborted the batch would be a claim
// about an order this side cannot see, so nothing here makes it. The sort below
// is for STABILITY alone — the same cycle must name the same bank between two
// renders — and not an attempt to reproduce the agent's order.
//
// A cycle whose payers can all cover shows nothing here, and that is NOT a claim
// that it will settle. It may simply not have been answered yet; the two are
// indistinguishable from this side, because what distinguishes them is a message
// this console never sees.
function shortfallOf(cycle: ClearingCycle, reserves: Reserve[]): Shortfall | null {
  const positions = Object.entries(cycle.netPositions ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );
  for (const [participant, net] of positions) {
    if (net >= 0) continue; // a net receiver is paid, not asked to pay
    const owed = -net;
    // In the cycle's OWN asset. A bank's euro reserve says nothing about
    // whether it can cover a dollar position, which is the whole reason a
    // reserve row carries its asset code.
    const held = reserves.find(
      (r) => r.participant === participant && r.asset === cycle.asset,
    );
    if (held === undefined) return { participant, owed, held: 0 };
    if (held.reserve < owed) return { participant, owed, held: held.reserve };
  }
  return null;
}

// A closed cycle is a settlement instruction that has arrived: the clearing
// house netted the payments, computed each bank's position, and sent a pacs.009
// naming the cycle. Discharging it is the central bank's act, and it is an
// automatic answer rather than a button — which is why these cards are read-only
// and why a cycle still sitting here has a story attached.
//
// The cycle's payment ids are deliberately not rendered. The central bank
// settles a net figure per bank and has no business with who paid whom.
function SettlementInstructions({
  cycles,
  reserves,
  isLoading,
  error,
  onRetry,
}: {
  cycles: ClearingCycle[];
  reserves: Reserve[];
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  return (
    <section className="space-y-3">
      <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
        Settlement instructions
        <Hint id="clearing-vs-settlement" />
      </h2>
      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : isLoading ? (
        <Card>
          <CardContent className="py-8 text-sm text-muted-foreground">Loading…</CardContent>
        </Card>
      ) : cycles.length === 0 ? (
        <Card>
          <CardContent className="py-8 text-sm text-muted-foreground">
            Nothing outstanding. A closed cycle appears here only while it is
            unsettled — the clearing house sends the instruction when it reaches
            the cut-off, and a discharged one moves to Settlements below.
          </CardContent>
        </Card>
      ) : (
        cycles.map((c) => (
          <InstructionCard key={c.id} cycle={c} shortfall={shortfallOf(c, reserves)} />
        ))
      )}
    </section>
  );
}

function InstructionCard({
  cycle,
  shortfall,
}: {
  cycle: ClearingCycle;
  shortfall: Shortfall | null;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          Cycle {cycle.scheme}
          <IdText id={cycle.id} />
          <EnumBadge value={cycle.status} />
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {shortfall ? (
          <RefusedAlert cycle={cycle} shortfall={shortfall} />
        ) : (
          <p className="text-sm text-muted-foreground">
            Closed and not settled. The clearing house sends a{" "}
            <code>pacs.009</code> carrying these positions, and the central bank
            answers it; a discharged cycle moves to Settlements below.
          </p>
        )}
        <NetPositionsTable positions={cycle.netPositions} asset={cycle.asset} />
      </CardContent>
    </Card>
  );
}

// RefusedAlert is the state this console could not show before: an instruction
// the central bank answered RJCT/AM04.
//
// Both numbers are on screen because the operator's next move depends on the
// gap and not on the fact — the member has to be funded by the difference before
// the cut-off can be instructed again. AM04 is named because it is what actually
// went on the wire, and it is the only thing a bank's own exception queue will
// show.
function RefusedAlert({ cycle, shortfall }: { cycle: ClearingCycle; shortfall: Shortfall }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(cycle.asset);
  const amount = (n: number) =>
    asset ? (
      <Money amount={n} asset={asset} />
    ) : (
      <UnresolvedAmount code={cycle.asset} isLoading={isLoading} />
    );

  return (
    <Alert variant="destructive">
      <AlertTitle>Not settled — a net payer cannot cover its position</AlertTitle>
      <AlertDescription>
        <p>
          <IdText id={shortfall.participant} /> owes {amount(shortfall.owed)} and
          holds {amount(shortfall.held)} on reserve, so the central bank answers
          this instruction <strong>RJCT/AM04</strong>{" "}
          and posts nothing at all. Settlement is one unit of work over the whole batch: every other
          member&rsquo;s position is undischarged too, and every payment in the
          cycle is still Cleared with the payer&rsquo;s money sitting in its own
          bank&rsquo;s clearing suspense. Fund the member, and the clearing house
          can instruct the cut-off again.
        </p>
      </AlertDescription>
    </Alert>
  );
}

// The settlements this central bank performed, read from its own listener.
//
// It is the console's record of what it answered ACSC to, and it is what makes
// "closed and still in the section above" mean something: a cycle either moved
// reserves and appears here, or it did not and is still up there.
function Settlements({
  settlements,
  isLoading,
  error,
  onRetry,
}: {
  settlements: Settlement[];
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  const columns: Column<Settlement>[] = [
    { key: "id", header: "Settlement", render: (s) => <IdText id={s.id} /> },
    { key: "cycle", header: "Cycle", render: (s) => <IdText id={s.cycleId} /> },
    { key: "asset", header: "Asset", render: (s) => s.asset },
    { key: "settledAt", header: "Settled", render: (s) => s.settledAt },
  ];
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-medium text-muted-foreground">Settlements</h2>
      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : (
        <DataTable
          columns={columns}
          rows={settlements}
          rowKey={(s) => s.id}
          isLoading={isLoading}
          empty="Nothing settled yet. Reserves move when the clearing house instructs a cut-off and this central bank answers it."
        />
      )}
    </section>
  );
}
