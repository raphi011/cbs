"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { toast } from "sonner";

import { AccountRef } from "@/components/account-ref";
import { AmortizationSchedule } from "@/components/amortization-schedule";
import { ArrearsBadge } from "@/components/arrears-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { FieldLabel } from "@/components/field-label";
import { Hint } from "@/components/hint";
import { IdText } from "@/components/id-text";
import { Input } from "@/components/ui/input";
import { Money } from "@/components/money";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAssetLookup,
  useChargeFacilityInterest,
  useFacility,
  useFacilitySchedule,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { formatDate, todayDateString } from "@/lib/dates";
import { FACILITY_KIND_LABEL } from "@/lib/enums";
import { formatRate } from "@/lib/rate";
import type { AssetScale } from "@/lib/money";
import type { Facility } from "@/lib/types";

// --- Facility figures -------------------------------------------------------

// Takes the already-loaded facility and asset as props rather than
// refetching: the page below has already resolved both by the time this
// renders, and useFacility's query-key caching would only dedupe a second
// call, not add anything a prop doesn't already give it.
function FigureCard({ f, asset }: { f: Facility; asset: AssetScale }) {
  const stats: { label: string; amount: number }[] = [
    { label: "Commitment", amount: f.commitment },
    { label: "Drawn", amount: f.drawn },
    { label: "Accrued interest", amount: f.accruedInterest },
    { label: "Outstanding", amount: f.outstanding },
  ];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          Balance
          <Hint id="credit-facility" />
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-2 gap-y-3 divide-x sm:grid-cols-4">
          {stats.map((s) => (
            <div key={s.label} className="px-4 first:pl-0">
              <div className="text-xs text-muted-foreground">{s.label}</div>
              <div className="mt-0.5 text-lg font-semibold tabular-nums">
                <Money amount={s.amount} asset={asset} />
              </div>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

// --- Billing cycle (revolving lines only) -----------------------------------

function ChargeInterestCard({ pid, fid }: { pid: string; fid: string }) {
  const charge = useChargeFacilityInterest(pid, fid);
  const [date, setDate] = useState(todayDateString());

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    try {
      const tx = await charge.mutateAsync({ date });
      // A cycle with nothing accrued and nothing drawn posts nothing: the
      // backend answers 200 with a completely empty body rather than a
      // Transaction with an empty id (see api/handlers_lending.go's
      // handleChargeInterest), and endpoints.ts's chargeFacilityInterest
      // types that as `Transaction | undefined` rather than `Transaction` —
      // check for it before treating the result as a posted transaction.
      if (tx) {
        toast.success(`Charged — posted ${tx.id}`);
      } else {
        toast.info("Nothing to charge this cycle");
      }
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          Billing cycle
          <Hint id="amortization" />
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="flex items-end gap-2">
          <div className="space-y-1">
            <FieldLabel htmlFor="charge-date">Date</FieldLabel>
            <Input
              id="charge-date"
              type="date"
              value={date}
              onChange={(e) => setDate(e.target.value)}
              className="h-9"
            />
          </div>
          <Button type="submit" size="sm" disabled={charge.isPending || !date}>
            {charge.isPending ? "Charging…" : "Charge interest"}
          </Button>
        </form>
        <p className="mt-2 text-xs text-muted-foreground">
          Capitalizes accrued interest into drawn principal and bills a new
          instalment for the cycle&apos;s minimum payment.
        </p>
      </CardContent>
    </Card>
  );
}

// facilityTerms renders the rate/day-count/term line as plain text — every
// value in it is already a formatted string (formatRate, formatDate), so
// there is nothing here that needs JSX, unlike the GL-account line above.
function facilityTerms(f: Facility): string {
  const parts = [
    `rate ${formatRate(f.rate, f.rateScale)}`,
    f.dayCount,
    f.kind === "TermLoan"
      ? `${f.method} over ${f.termMonths} months`
      : f.minPayment != null
        ? `minimum payment ${formatRate(f.minPayment, f.rateScale)} of drawn balance`
        : undefined,
    `opened ${formatDate(f.openedAt)}`,
    // maturityAt is a Go time.Time: `omitempty` never elides its zero value
    // (encoding/json's omitempty doesn't apply to a zero struct), so this is
    // always a timestamp — the zero-date sentinel until a term loan is
    // disbursed. formatDate renders that sentinel as "—", but only a
    // TermLoan has a maturity concept at all; a RevolvingLine never carries
    // one, so gate on kind rather than showing a nonsensical "matures —" for
    // a product that was never going to have one.
    f.kind === "TermLoan" ? `matures ${formatDate(f.maturityAt)}` : undefined,
  ].filter((p): p is string => p !== undefined);
  return parts.join(" · ");
}

// --- Page --------------------------------------------------------------------

export default function FacilityDetailPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const fid = typeof params.fid === "string" ? params.fid : "";

  const { data: f, isLoading, error, refetch } = useFacility(pid, fid);
  const { data: schedule, isLoading: scheduleLoading } = useFacilitySchedule(
    pid,
    fid,
  );
  const { byCode, isLoading: assetsLoading } = useAssetLookup();
  const asset = f ? byCode.get(f.asset) : undefined;

  const back = `/participants/${pid}/facilities`;

  if (error) {
    return (
      <div className="space-y-4">
        <Link
          href={back}
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-4" /> Facilities
        </Link>
        <ErrorState error={error} onRetry={() => refetch()} />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <Link
        href={back}
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Facilities
      </Link>

      {isLoading || assetsLoading || !f ? (
        <Skeleton className="h-10 w-64" />
      ) : !asset ? (
        <ErrorState
          error={
            new Error(
              `This facility is denominated in "${f.asset}", which the system has no definition for, so its amounts cannot be rendered at a known scale.`,
            )
          }
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">
                {f.name}
              </h2>
              <span className="text-sm text-muted-foreground">
                {FACILITY_KIND_LABEL[f.kind]}
              </span>
              <EnumBadge value={f.status} />
              <IdText id={f.id} />
            </div>
            <ArrearsBadge bucket={f.arrearsBucket} nonPerforming={f.nonPerforming} />
          </div>

          <p className="flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
            Principal account <AccountRef pid={pid} id={f.principalGlAccount} /> ·
            interest account <AccountRef pid={pid} id={f.interestGlAccount} />
          </p>

          <p className="text-sm text-muted-foreground">{facilityTerms(f)}</p>

          <FigureCard f={f} asset={asset} />

          {f.kind === "RevolvingLine" && f.status !== "Closed" && (
            <ChargeInterestCard pid={pid} fid={fid} />
          )}

          <AmortizationSchedule
            installments={schedule}
            asset={asset}
            isLoading={scheduleLoading}
          />
        </>
      )}
    </div>
  );
}
