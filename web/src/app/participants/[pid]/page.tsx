"use client";

import { useParams } from "next/navigation";

import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Money } from "@/components/money";
import { CopyId } from "@/components/copy-id";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { useParticipant, useReserve } from "@/lib/api/hooks";
import type { HintKey } from "@/components/hint-content";

export default function ParticipantOverview() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data: p } = useParticipant(pid);
  const { data: reserve, isLoading: reserveLoading } = useReserve(pid);

  const accounts: { label: string; id: string; hint: HintKey }[] = p
    ? [
        {
          label: "Customer subledger",
          id: p.customerSubledger,
          hint: "ledger-vs-subledger",
        },
        {
          label: "Clearing suspense",
          id: p.suspenseAccount,
          hint: "clearing-suspense",
        },
        {
          label: "Reserve at central bank",
          id: p.reserveAccount,
          hint: "reserve-account",
        },
        {
          label: "Settlement (central-bank ledger)",
          id: p.settlementAccount,
          hint: "central-bank-reserves",
        },
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
            <p className="text-2xl font-semibold">
              <Money cents={reserve?.reserve ?? 0} />
            </p>
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
              <CopyId id={a.id} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
