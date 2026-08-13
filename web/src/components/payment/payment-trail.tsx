"use client";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { formatBusinessDate } from "@/lib/dates";
import { usePaymentAudit } from "@/lib/api/hooks";
import { trailOf, waitingOn, type TrailStep } from "@/lib/payment-trail";
import type { PaymentStatus } from "@/lib/enums";

// Where a payment has been, as ONE institution's copy of it tells it.
//
// Nothing here merges two institutions' answers, and that refusal is the
// design: three copies in three databases legitimately disagree, and a single
// stitched timeline would be this app quietly asserting they do not. Seeing the
// other side means changing seat.
//
// The DAY is rendered and never the time. A deployment's clock does not move
// within a business day, so every step of one carries the same instant.

export function PaymentTrail({
  payid,
  status,
  institution,
}: {
  payid: string;
  status: PaymentStatus;
  // Whose copy this is, in words a banker would use — "the clearing house".
  institution: string;
}) {
  const { data, isLoading, error, refetch } = usePaymentAudit({ entity: payid });
  const steps = trailOf(data ?? []);
  const waiting = waitingOn(status);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          Trail
          <Hint id="payment-lifecycle" />
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {error ? (
          <ErrorState error={error} onRetry={() => void refetch()} />
        ) : isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : steps.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Nothing is recorded about this payment at {institution}.
          </p>
        ) : (
          <>
            <ol className="space-y-0">
              {steps.map((step, i) => (
                <Step
                  key={step.seq}
                  step={step}
                  last={i === steps.length - 1}
                  waiting={i === steps.length - 1 ? waiting : undefined}
                />
              ))}
            </ol>
            <p className="border-t pt-3 text-xs text-muted-foreground">
              {`What ${institution} was told, out of its own record. The payer's bank and the payee's hold copies of their own, and the three may disagree.`}
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}

// One step: the marker and its line down the left, then what the state means.
// The line stops at the last step, because a trail does not claim there is
// another one coming.
function Step({
  step,
  last,
  waiting,
}: {
  step: TrailStep;
  last: boolean;
  waiting?: { because: string; act: string };
}) {
  return (
    <li className="flex gap-3">
      <div className="flex flex-col items-center pt-1.5">
        <span className="size-2 shrink-0 rounded-full bg-primary" />
        {!last && <span className="w-px flex-1 bg-border" />}
      </div>
      <div className={last && !waiting ? "space-y-1" : "space-y-1 pb-4"}>
        <div className="flex items-baseline gap-2">
          <span className="text-sm font-medium">{step.title}</span>
          {step.hint && <Hint id={step.hint} />}
          <span className="text-xs text-muted-foreground">
            {formatBusinessDate(step.day)}
          </span>
        </div>
        {step.body && <p className="text-xs text-muted-foreground">{step.body}</p>}
        {waiting && (
          // What would move it, and whose act that is. The act is named in the
          // words of the button that runs it and never as a phase, a route or a
          // function: the reader has to be able to find it.
          <p className="mt-1.5 rounded-md bg-muted px-2 py-1.5 text-xs">
            {waiting.because}{" "}
            <span className="font-medium">▸ {waiting.act}</span>
          </p>
        )}
      </div>
    </li>
  );
}
