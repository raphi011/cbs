"use client";

import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { CopyId } from "@/components/copy-id";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { NetPositionsTable } from "@/components/net-positions-table";
import { ConfirmAction } from "@/components/forms/confirm-action";
import {
  useCloseCycle,
  useCycle,
  useSettleCycle,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { formatDateTime } from "@/lib/dates";

export default function CycleDetailPage() {
  const params = useParams();
  const router = useRouter();
  const cid = typeof params.cid === "string" ? params.cid : "";
  const { data: c, isLoading, error, refetch } = useCycle(cid);
  const closeCycle = useCloseCycle();
  const settle = useSettleCycle();

  return (
    <div className="space-y-5">
      <Link
        href="/cycles"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Clearing cycles
      </Link>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : isLoading || !c ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">
                Cycle {c.scheme}
              </h2>
              <CopyId id={c.id} />
              <EnumBadge value={c.status} />
            </div>
            <div className="flex items-center gap-2">
              {c.status === "Open" && (
                <ConfirmAction
                  trigger={
                    <Button size="sm">Close &amp; net</Button>
                  }
                  title="Close cycle"
                  description="Stops accepting payments and computes each participant's net position."
                  confirmLabel="Close"
                  pending={closeCycle.isPending}
                  onConfirm={async () => {
                    await closeCycle.mutateAsync(c.id, {
                      onSuccess: () => toast.success("Cycle closed — positions netted"),
                      onError: (err) => toast.error(describeError(err)),
                    });
                  }}
                />
              )}
              {c.status === "Closed" && (
                <ConfirmAction
                  trigger={<Button size="sm">Settle</Button>}
                  title="Settle cycle"
                  description="Moves the net amounts across central-bank reserves, discharging the obligations."
                  confirmLabel="Settle"
                  pending={settle.isPending}
                  onConfirm={async () => {
                    const s = await settle.mutateAsync(c.id, {
                      onError: (err) => toast.error(describeError(err)),
                    });
                    toast.success("Cycle settled");
                    if (s) router.push(`/settlements/${s.id}`);
                  }}
                />
              )}
            </div>
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">Timeline</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-2 text-sm sm:grid-cols-3">
              <div>
                <div className="text-muted-foreground">Opened</div>
                <div>{formatDateTime(c.openedAt)}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Closed</div>
                <div>{formatDateTime(c.closedAt)}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Settlement</div>
                <div>
                  {c.settlementId ? (
                    <Link
                      href={`/settlements/${c.settlementId}`}
                      className="font-mono underline-offset-2 hover:underline"
                    >
                      {c.settlementId}
                    </Link>
                  ) : (
                    "—"
                  )}
                </div>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5 text-base">
                Net positions
                <Hint id="net-positions" />
              </CardTitle>
            </CardHeader>
            <CardContent>
              <NetPositionsTable positions={c.netPositions} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">
                Payments ({c.paymentIds.length})
              </CardTitle>
            </CardHeader>
            <CardContent>
              {c.paymentIds.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No payments routed into this cycle.
                </p>
              ) : (
                <div className="flex flex-wrap gap-2">
                  {c.paymentIds.map((id) => (
                    <Link
                      key={id}
                      href={`/payments/${id}`}
                      className="rounded-md border px-2 py-1 font-mono text-xs underline-offset-2 hover:underline"
                    >
                      {id}
                    </Link>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}
