"use client";

import { useParams } from "next/navigation";
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
import { Money } from "@/components/money";
import { ConfirmAction } from "@/components/forms/confirm-action";
import {
  usePayment,
  useRejectPayment,
  useReturnPayment,
  useSchemes,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { formatDateTime } from "@/lib/dates";
import type { HintKey } from "@/components/hint-content";

function Row({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: HintKey;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-3 border-b py-2 last:border-0">
      <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
        {label}
        {hint && <Hint id={hint} />}
      </span>
      <span className="text-right text-sm font-medium">{children}</span>
    </div>
  );
}

export default function PaymentDetailPage() {
  const params = useParams();
  const payid = typeof params.payid === "string" ? params.payid : "";
  const { data: p, isLoading, error, refetch } = usePayment(payid);
  const schemes = useSchemes();
  const reject = useRejectPayment();
  const ret = useReturnPayment();

  const scheme = schemes.data?.find((s) => s.id === p?.scheme);
  // Reject is for in-flight payments (before money has settled); return unwinds
  // a completed one, and only if the scheme allows it.
  const canReject =
    p != null && ["Initiated", "Accepted", "Cleared"].includes(p.status);
  const canReturn = p?.status === "Settled" && (scheme?.allowsReturn ?? false);

  return (
    <div className="space-y-5">
      <Link
        href="/payments"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Payments
      </Link>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : isLoading || !p ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">Payment</h2>
              <CopyId id={p.id} />
              <EnumBadge value={p.status} />
            </div>
            <div className="flex items-center gap-2">
              {canReject && (
                <ConfirmAction
                  trigger={
                    <Button variant="destructive" size="sm">
                      Reject
                    </Button>
                  }
                  title="Reject payment"
                  description="Declines the in-flight payment before it settles."
                  confirmLabel="Reject"
                  destructive
                  pending={reject.isPending}
                  input={{ label: "Reason", placeholder: "e.g. insufficient funds", required: true }}
                  onConfirm={async (reason) => {
                    await reject.mutateAsync(
                      { payid: p.id, reason },
                      {
                        onSuccess: () => toast.success("Payment rejected"),
                        onError: (err) => toast.error(describeError(err)),
                      },
                    );
                  }}
                />
              )}
              {canReturn && (
                <ConfirmAction
                  trigger={
                    <Button variant="outline" size="sm">
                      Return
                    </Button>
                  }
                  title="Return payment"
                  description="Unwinds a settled payment, sending the funds back to the debtor."
                  confirmLabel="Return"
                  pending={ret.isPending}
                  input={{ label: "Reason", placeholder: "e.g. account closed", required: true }}
                  onConfirm={async (reason) => {
                    await ret.mutateAsync(
                      { payid: p.id, reason },
                      {
                        onSuccess: () => toast.success("Payment returned"),
                        onError: (err) => toast.error(describeError(err)),
                      },
                    );
                  }}
                />
              )}
            </div>
          </div>

          <div className="grid gap-4 md:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Details</CardTitle>
              </CardHeader>
              <CardContent className="py-0">
                <Row label="Scheme">{p.scheme}</Row>
                <Row label="Amount">
                  <Money cents={p.amount} />
                </Row>
                {p.mandateId && (
                  <Row label="Mandate" hint="requires-mandate">
                    <CopyId id={p.mandateId} />
                  </Row>
                )}
                {p.endToEndId && <Row label="End-to-end ID">{p.endToEndId}</Row>}
                {p.description && <Row label="Description">{p.description}</Row>}
                {p.rejectReason && (
                  <Row label="Reason">{p.rejectReason}</Row>
                )}
                <Row label="Value date" hint="value-date">
                  {formatDateTime(p.valueDate)}
                </Row>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">Parties &amp; legs</CardTitle>
              </CardHeader>
              <CardContent className="py-0">
                <Row label="Debtor" hint="debtor-leg">
                  <span className="flex flex-col items-end gap-0.5">
                    <CopyId id={p.debtor.participant} />
                    <CopyId id={p.debtor.account} />
                  </span>
                </Row>
                <Row label="Creditor" hint="creditor-leg">
                  <span className="flex flex-col items-end gap-0.5">
                    <CopyId id={p.creditor.participant} />
                    <CopyId id={p.creditor.account} />
                  </span>
                </Row>
                {p.debtorLegTx && (
                  <Row label="Debtor leg tx">
                    <CopyId id={p.debtorLegTx} />
                  </Row>
                )}
                {p.creditorLegTx && (
                  <Row label="Creditor leg tx">
                    <CopyId id={p.creditorLegTx} />
                  </Row>
                )}
                {p.cycleId && (
                  <Row label="Clearing cycle" hint="clearing-vs-settlement">
                    <Link
                      href={`/cycles/${p.cycleId}`}
                      className="font-mono text-sm underline-offset-2 hover:underline"
                    >
                      {p.cycleId}
                    </Link>
                  </Row>
                )}
              </CardContent>
            </Card>
          </div>
        </>
      )}
    </div>
  );
}
