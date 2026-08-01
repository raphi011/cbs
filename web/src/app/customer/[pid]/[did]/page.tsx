"use client";

import { useParams } from "next/navigation";
import { Snowflake } from "lucide-react";

import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Money } from "@/components/money";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAssetLookup,
  useDepositAccount,
  useDepositBalance,
  useParticipant,
} from "@/lib/api/hooks";

// What a customer sees of their own account. No GL account, no product id, no
// pricing source, no internal ids — those are the bank's business and live in
// the back office. Every call here goes to their bank's listener: a customer is
// a view onto a bank, not an institution with a backend of its own.
export default function CustomerOverview() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, isLoading, error, refetch } = useDepositAccount(pid, did);
  const { data: balance, isLoading: balanceLoading } = useDepositBalance(pid, did);
  const { data: bank } = useParticipant(pid);
  const { byCode } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;
  const iban = account?.identifiers.find((i) => i.scheme === "IBAN");

  if (error) return <ErrorState error={error} onRetry={() => refetch()} />;
  if (isLoading || !account || !asset) return <Skeleton className="h-64 w-full" />;

  // The headroom below zero, which a customer thinks of as part of what they can
  // spend and the bank does not: the available balance already includes it.
  const headroom = account.overdraftLimit;

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">{account.name}</h1>
        <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          {bank?.name}
          {iban && <span className="font-mono">{iban.value}</span>}
          <EnumBadge value={account.status} />
        </p>
      </div>

      {account.status === "Frozen" && (
        <Alert>
          <Snowflake className="size-4" />
          <AlertTitle>This account is frozen</AlertTitle>
          <AlertDescription>
            No money can leave it until your bank unfreezes it. Money can still
            arrive — an incoming payment is a credit, and the block is on debits.
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardContent className="space-y-4">
          <div>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Available
              <Hint id="balance-available" />
            </p>
            {!balanceLoading && balance ? (
              <p className="text-3xl font-semibold tabular-nums">
                <Money amount={balance.available} asset={asset} />
              </p>
            ) : (
              <Skeleton className="mt-1 h-9 w-32" />
            )}
          </div>
          <div className="grid grid-cols-2 gap-4 border-t pt-4">
            <div>
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                Balance
                <Hint id="balance-book" />
              </p>
              {!balanceLoading && balance ? (
                <p className="text-lg font-medium tabular-nums">
                  <Money amount={balance.book} asset={asset} />
                </p>
              ) : (
                <Skeleton className="mt-1 h-7 w-20" />
              )}
            </div>
            <div>
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                On hold
                <Hint id="balance-holds" />
              </p>
              {!balanceLoading && balance ? (
                <p className="text-lg font-medium tabular-nums">
                  <Money amount={balance.holds} asset={asset} />
                </p>
              ) : (
                <Skeleton className="mt-1 h-7 w-20" />
              )}
            </div>
          </div>
          {headroom > 0 && (
            <p className="flex items-center gap-1.5 border-t pt-4 text-sm text-muted-foreground">
              Includes an arranged overdraft of <Money amount={headroom} asset={asset} />
              <Hint id="overdraft-interest" />
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
