"use client";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { DataTable, type Column } from "@/components/data-table";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Money } from "@/components/money";
import { useOverdraftTerms } from "@/lib/api/hooks";
import { formatDate } from "@/lib/dates";
import { formatRate } from "@/lib/rate";
import type { Asset, OverdraftTerms } from "@/lib/types";

// Read-only view of an account's whole effective-dated overdraft terms
// timeline, oldest first — including the opening row every account gets at
// OpenAccount. There is no form here: the only way to add a row is
// POST .../overdraft-terms, which this card exists to make the history of
// inspectable rather than merely recoverable by replaying the audit log.
export function OverdraftTermsCard({
  pid,
  did,
  asset,
}: {
  pid: string;
  did: string;
  asset: Asset;
}) {
  const { data, isLoading, error, refetch } = useOverdraftTerms(pid, did);

  const columns: Column<OverdraftTerms>[] = [
    {
      key: "effectiveFrom",
      header: "Effective from",
      render: (t) => formatDate(t.effectiveFrom),
    },
    {
      key: "overdraftLimit",
      header: "Limit",
      render: (t) => <Money amount={t.overdraftLimit} asset={asset} />,
    },
    {
      key: "rate",
      header: "Arranged",
      render: (t) => formatRate(t.rate, t.rateScale),
    },
    {
      key: "unarrangedRate",
      header: "Unarranged",
      render: (t) => formatRate(t.unarrangedRate, t.rateScale),
    },
    { key: "dayCount", header: "Day count", render: (t) => t.dayCount },
    {
      key: "createdAt",
      header: "Entered",
      render: (t) => formatDate(t.createdAt),
    },
  ];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          Overdraft terms
          <Hint id="effective-dated-terms" />
        </CardTitle>
      </CardHeader>
      <CardContent>
        {error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : (
          <DataTable
            columns={columns}
            rows={data}
            rowKey={(t) => t.effectiveFrom}
            isLoading={isLoading}
            empty="No terms yet."
          />
        )}
      </CardContent>
    </Card>
  );
}
