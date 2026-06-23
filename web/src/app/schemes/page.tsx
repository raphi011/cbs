"use client";

import { PageHeader } from "@/components/page-header";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Hint } from "@/components/hint";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { useSchemes } from "@/lib/api/hooks";
import type { HintKey } from "@/components/hint-content";

function DetailRow({
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
      <span className="text-sm font-medium">{children}</span>
    </div>
  );
}

export default function SchemesPage() {
  const { data, isLoading, error } = useSchemes();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Payment schemes"
        hint="clearing-vs-settlement"
        description="Each scheme is a payment product with its own rules. Direction governs who initiates; money always flows debtor → creditor."
      />

      {error ? (
        <ErrorState error={error} />
      ) : isLoading ? (
        <div className="grid gap-4 md:grid-cols-2">
          <Skeleton className="h-56" />
          <Skeleton className="h-56" />
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {data?.map((s) => (
            <Card key={s.id}>
              <CardHeader>
                <CardTitle className="font-mono text-base">{s.id}</CardTitle>
              </CardHeader>
              <CardContent className="py-0">
                <DetailRow
                  label="Direction"
                  hint={
                    s.direction === "Push"
                      ? "scheme-direction-push"
                      : "scheme-direction-pull"
                  }
                >
                  {s.direction}
                </DetailRow>
                <DetailRow
                  label="Settlement model"
                  hint={
                    s.settlementModel === "Net"
                      ? "settlement-model-net"
                      : "settlement-model-gross"
                  }
                >
                  {s.settlementModel}
                </DetailRow>
                <DetailRow label="Requires mandate" hint="requires-mandate">
                  {s.requiresMandate ? "Yes" : "No"}
                </DetailRow>
                <DetailRow label="Allows return" hint="allows-return">
                  {s.allowsReturn ? "Yes" : "No"}
                </DetailRow>
                <DetailRow label="Settlement delay" hint="settlement-delay">
                  {s.settlementDelay}
                </DetailRow>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
