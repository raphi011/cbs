"use client";

import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { CreateParticipantDialog } from "@/components/create-participant-dialog";
import { DataTable, type Column } from "@/components/data-table";
import { AmountCell, UnresolvedAmount } from "@/components/money";
import { IdText } from "@/components/id-text";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NetPositionsTable } from "@/components/net-positions-table";
import { ConfirmAction } from "@/components/forms/confirm-action";
import {
  useAssetLookup,
  useCentralBankCycles,
  useReserves,
  useSettleCycle,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { ClearingCycle, Reserve } from "@/lib/types";

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

// The settlement layer. Banks meet only here, and what the central bank does is
// move reserves between them — it never sees an individual payment, which is
// what the operator split made expressible for the first time.
//
// Admission is the central bank's act too: opening a member's reserve and
// settlement accounts happens in the central bank's own book, which is why
// POST /members is its route and not the clearing house's.
export default function CentralBankPage() {
  const reserves = useReserves();
  const cycles = useCentralBankCycles();

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
        isLoading={cycles.isLoading}
        error={cycles.error}
        onRetry={() => cycles.refetch()}
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

// A closed cycle is a settlement instruction: the clearing house netted the
// payments and computed each bank's position, and discharging those positions is
// the central bank's act. That is why the settle button is here and not on the
// cycle page — a clearing house that could move reserves would be a central bank.
//
// The cycle's payment ids are deliberately not rendered. The central bank
// settles a net figure per bank and has no business with who paid whom.
function SettlementInstructions({
  cycles,
  isLoading,
  error,
  onRetry,
}: {
  cycles: ClearingCycle[];
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
            Nothing to settle. A cycle becomes settleable once the clearing house
            closes it and nets the positions.
          </CardContent>
        </Card>
      ) : (
        cycles.map((c) => <SettleCard key={c.id} cycle={c} />)
      )}
    </section>
  );
}

function SettleCard({ cycle }: { cycle: ClearingCycle }) {
  const settle = useSettleCycle();
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-base">
          Cycle {cycle.scheme}
          <IdText id={cycle.id} />
          <EnumBadge value={cycle.status} />
        </CardTitle>
        <ConfirmAction
          trigger={<Button size="sm">Settle</Button>}
          title="Settle cycle"
          description="Moves the net amounts across central-bank reserves, discharging the obligations."
          confirmLabel="Settle"
          pending={settle.isPending}
          onConfirm={async () => {
            await settle.mutateAsync(cycle.id, {
              onError: (err) => toast.error(describeError(err)),
            });
            toast.success("Cycle settled — reserves moved");
          }}
        />
      </CardHeader>
      <CardContent>
        <NetPositionsTable positions={cycle.netPositions} asset={cycle.asset} />
      </CardContent>
    </Card>
  );
}
