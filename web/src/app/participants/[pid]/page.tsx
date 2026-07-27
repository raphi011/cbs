"use client";

import { useParams } from "next/navigation";

import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Money } from "@/components/money";
import { IdText } from "@/components/id-text";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { useParticipant, useReserve } from "@/lib/api/hooks";
import type { HintKey } from "@/components/hint-content";

export default function ParticipantOverview() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data: p } = useParticipant(pid);
  const { data: reserve, isLoading: reserveLoading } = useReserve(pid);

  // A bank holds one suspense, reserve and settlement account per asset it
  // operates in, so the list is the customer subledger plus three rows per
  // asset. A euro-only bank looks exactly as it always did.
  const accounts: { label: string; id: string; hint: HintKey }[] = p
    ? [
        {
          label: "Customer subledger",
          id: p.customerSubledger,
          hint: "ledger-vs-subledger" as HintKey,
        },
        ...(p.assets ?? []).flatMap((a) => [
          {
            label: `Clearing suspense (${a.asset})`,
            id: a.suspense,
            hint: "clearing-suspense" as HintKey,
          },
          {
            label: `Reserve at central bank (${a.asset})`,
            id: a.reserve,
            hint: "reserve-account" as HintKey,
          },
          {
            label: `Settlement, central-bank ledger (${a.asset})`,
            id: a.settlement,
            hint: "central-bank-reserves" as HintKey,
          },
        ]),
      ]
    : [];

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            Central-bank reserves
            <Hint id="central-bank-reserves" />
          </CardTitle>
        </CardHeader>
        <CardContent>
          {reserveLoading ? (
            <Skeleton className="h-8 w-32" />
          ) : (
            (reserve ?? []).map((r) => (
              <p key={r.asset} className="text-2xl font-semibold">
                <Money cents={r.reserve} />{" "}
                <span className="text-sm font-normal text-muted-foreground">
                  {r.asset}
                </span>
              </p>
            ))
          )}
          <p className="mt-1 text-sm text-muted-foreground">
            Starts at €0.00. Funding a deposit account raises this in step —
            funding is modelled as the bank placing cash on reserve.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Internal accounts</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {accounts.map((a) => (
            <div
              key={a.label}
              className="flex items-center justify-between gap-3"
            >
              <span className="flex items-center gap-1.5 text-sm">
                {a.label}
                <Hint id={a.hint} />
              </span>
              <IdText id={a.id} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
