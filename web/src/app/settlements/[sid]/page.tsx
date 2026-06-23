"use client";

import { useParams } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { CopyId } from "@/components/copy-id";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { NetPositionsTable } from "@/components/net-positions-table";
import { useSettlement } from "@/lib/api/hooks";
import { formatDateTime } from "@/lib/dates";

export default function SettlementDetailPage() {
  const params = useParams();
  const sid = typeof params.sid === "string" ? params.sid : "";
  const { data: s, isLoading, error, refetch } = useSettlement(sid);

  return (
    <div className="space-y-5">
      <Link
        href="/settlements"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Settlements
      </Link>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : isLoading || !s ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <div className="flex items-center gap-2">
            <h2 className="text-xl font-semibold tracking-tight">Settlement</h2>
            <CopyId id={s.id} />
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">Details</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-2 text-sm sm:grid-cols-3">
              <div>
                <div className="text-muted-foreground">Cycle</div>
                <Link
                  href={`/cycles/${s.cycleId}`}
                  className="font-mono underline-offset-2 hover:underline"
                >
                  {s.cycleId}
                </Link>
              </div>
              <div className="flex items-center gap-1">
                <div>
                  <div className="text-muted-foreground">Settlement tx</div>
                  <CopyId id={s.settlementTx} />
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">Value date</div>
                <div>{formatDateTime(s.valueDate)}</div>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5 text-base">
                Net positions settled
                <Hint id="net-positions" />
              </CardTitle>
            </CardHeader>
            <CardContent>
              <NetPositionsTable positions={s.netPositions} />
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}
