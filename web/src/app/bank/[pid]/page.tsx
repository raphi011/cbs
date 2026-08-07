"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ChevronRight } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { IdText } from "@/components/id-text";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { LodgeReservesForm } from "@/components/forms/lodge-reserves-form";
import { Money, UnresolvedAmount } from "@/components/money";
import { Skeleton } from "@/components/ui/skeleton";
import { OpenDepositAccountForm } from "@/components/forms/open-deposit-account-form";
import {
  useAssetLookup,
  useDepositAccounts,
  useDepositBalance,
  useReserve,
  useTotals,
} from "@/lib/api/hooks";
import type { DepositAccount } from "@/lib/types";

function DepositAccountRow({ pid, account }: { pid: string; account: DepositAccount }) {
  const { data, isLoading: balanceLoading } = useDepositBalance(pid, account.id);
  const { byCode, isLoading: assetLoading } = useAssetLookup();
  const asset = byCode.get(account.asset);
  const iban = account.identifiers.find((i) => i.scheme === "IBAN");
  return (
    <Link
      href={`/bank/${pid}/deposit-accounts/${account.id}`}
      className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-sm font-medium">{account.name}</span>
        <EnumBadge value={account.status} />
        {iban ? (
          <span className="font-mono text-xs text-muted-foreground">{iban.value}</span>
        ) : (
          <IdText id={account.id} />
        )}
      </span>
      <span className="flex items-center gap-3">
        <span className="text-right text-sm font-medium">
          {!asset ? (
            <UnresolvedAmount
              code={account.asset}
              isLoading={assetLoading}
              className="ml-auto block text-right"
            />
          ) : balanceLoading || !data ? (
            // Never default to 0 while the balance is still in flight — a
            // fetched zero and a not-yet-fetched amount are different facts.
            <Skeleton className="ml-auto h-4 w-16" />
          ) : (
            <Money amount={data.available} asset={asset} />
          )}
          <span className="block text-xs font-normal text-muted-foreground">available</span>
        </span>
        <ChevronRight className="size-4 text-muted-foreground" />
      </span>
    </Link>
  );
}

// A back office opens on its customers. No card of raw internal-account ids: the
// chart of accounts is one click away under General ledger, and a bank's home is
// the people it holds money for.
export default function BankHome() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const accounts = useDepositAccounts(pid);
  const {
    data: totals,
    isLoading: totalsLoading,
    error: totalsError,
    refetch: refetchTotals,
  } = useTotals(pid);
  const {
    data: reserve,
    isLoading: reserveLoading,
    error: reserveError,
    refetch: refetchReserve,
  } = useReserve(pid);
  const { byCode, isLoading: assetLoading } = useAssetLookup();

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2">
        <Card size="sm">
          <CardContent>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Reserves at the central bank
              <Hint id="reserve-account" />
            </p>
            {reserveError ? (
              <ErrorState error={reserveError} onRetry={() => refetchReserve()} />
            ) : reserveLoading ? (
              <Skeleton className="mt-1 h-6 w-24" />
            ) : reserve && reserve.length > 0 ? (
              // One row per asset, and a Lodge button beside each: a lodgement
              // moves the bank's own cash, of which there is one pot per asset,
              // so this list is already the right shape to hang the action off.
              //
              // The button is HERE and not on the deposit-account page because a
              // lodgement is the bank's own treasury decision rather than
              // anything to do with a customer. Taking cash in is on the account
              // (see FundParticipantForm); putting it on reserve is the bank's.
              <div className="mt-1 space-y-0.5 text-xl font-semibold tabular-nums">
                {reserve.map((r) => {
                  const asset = byCode.get(r.asset);
                  return asset ? (
                    <div key={r.asset} className="flex items-center justify-between gap-2">
                      <p>
                        <Money amount={r.reserve} asset={asset} />
                      </p>
                      <LodgeReservesForm pid={pid} asset={asset} />
                    </div>
                  ) : (
                    <UnresolvedAmount key={r.asset} code={r.asset} isLoading={assetLoading} />
                  );
                })}
              </div>
            ) : (
              // No reserve ROWS at all means the central bank holds no account
              // for this bank, which is a bank the scheme has not answered for
              // yet. There is nothing to lodge into, so no button: the domain
              // would refuse it (ErrSettlementMemberNotFound) and the honest
              // thing is not to offer the action.
              <p className="mt-1 text-sm text-muted-foreground">No reserves yet.</p>
            )}
          </CardContent>
        </Card>
        <Card size="sm">
          <CardContent>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Customer deposits
              <Hint id="derived-balance" />
            </p>
            {totalsError ? (
              <ErrorState error={totalsError} onRetry={() => refetchTotals()} />
            ) : totalsLoading ? (
              <Skeleton className="mt-1 h-6 w-24" />
            ) : totals && totals.length > 0 ? (
              <div className="mt-1 space-y-0.5 text-xl font-semibold tabular-nums">
                {totals.map((t) => {
                  const asset = byCode.get(t.asset);
                  return asset ? (
                    <p key={t.asset}>
                      <Money amount={t.deposits} asset={asset} />
                      <span className="ml-2 text-xs font-normal text-muted-foreground">
                        less <Money amount={t.overdrafts} asset={asset} /> drawn
                      </span>
                    </p>
                  ) : (
                    <UnresolvedAmount key={t.asset} code={t.asset} isLoading={assetLoading} />
                  );
                })}
              </div>
            ) : (
              <p className="mt-1 text-sm text-muted-foreground">No customer deposits yet.</p>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="flex items-center gap-1.5 text-base">
            Customer accounts
            <Hint id="balance-available" />
          </CardTitle>
          <OpenDepositAccountForm pid={pid} />
        </CardHeader>
        <CardContent>
          {accounts.error ? (
            <ErrorState error={accounts.error} onRetry={() => accounts.refetch()} />
          ) : accounts.isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : accounts.data && accounts.data.length > 0 ? (
            <div className="divide-y rounded-lg border">
              {accounts.data.map((a) => (
                <DepositAccountRow key={a.id} pid={pid} account={a} />
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              No deposit accounts yet. Open one, then fund it to start the money loop.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
