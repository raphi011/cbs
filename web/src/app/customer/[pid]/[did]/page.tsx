"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import { Snowflake } from "lucide-react";

import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Money } from "@/components/money";
import { EnumBadge } from "@/components/enum-badge";
import { CopyButton } from "@/components/copy-button";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { StatementTable } from "@/components/statement/statement-table";
import {
  SendMoneyDialog,
  SendReceipt,
  type SendResult,
} from "@/components/forms/send-money-dialog";
import {
  useAssetLookup,
  useDepositAccount,
  useDepositBalance,
  useParticipant,
  useStatement,
} from "@/lib/api/hooks";

// What a customer sees of their own account, and it is ONE page: what they hold,
// what they can do with it, and what has happened to it. A retail app has no
// sections to navigate between — sending is an act, not a place, so it is a
// dialog over this page and its receipt comes back here.
//
// No GL account, no product id, no pricing source, no internal ids — those are
// the bank's business and live in the back office. Every call here goes to their
// bank's listener: a customer is a view onto a bank, not an institution with a
// backend of its own.
export default function CustomerAccount() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, isLoading, error, refetch } = useDepositAccount(pid, did);
  const { data: balance, isLoading: balanceLoading } = useDepositBalance(pid, did);
  const { data: bank } = useParticipant(pid);
  const { byCode, error: assetError, refetch: refetchAssets } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;
  const iban = account?.identifiers.find((i) => i.scheme === "IBAN");
  const statement = useStatement(pid, did, account?.controlAccount ?? "");
  const [sent, setSent] = useState<SendResult | null>(null);

  // The asset lookup failing is a distinct fact from it still being in
  // flight — folding it into the loading guard below would leave a customer
  // staring at a skeleton forever if GET /assets never answers.
  if (error || assetError) {
    return (
      <ErrorState
        error={error ?? assetError}
        onRetry={() => (error ? refetch() : refetchAssets())}
      />
    );
  }
  if (isLoading || !account || !asset) return <Skeleton className="h-64 w-full" />;

  // The headroom below zero, which a customer thinks of as part of what they can
  // spend and the bank does not: the available balance already includes it.
  const headroom = account.overdraftLimit;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight">{account.name}</h1>
          <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            {bank?.name}
            {/* The address a payer has to be given, so it is the one thing on
                this page worth copying: what a payee sends is this string. */}
            {iban && (
              <span className="flex items-center gap-1">
                <span className="font-mono">{iban.value}</span>
                <CopyButton value={iban.value} label="IBAN" />
              </span>
            )}
            <EnumBadge value={account.status} />
          </p>
        </div>
        <SendMoneyDialog
          pid={pid}
          did={did}
          account={account}
          asset={asset}
          onSent={setSent}
        />
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
      {account.status === "Closed" && (
        <Alert>
          <AlertTitle>This account is closed</AlertTitle>
          <AlertDescription>
            Closed is terminal — it cannot send or receive.
          </AlertDescription>
        </Alert>
      )}

      {sent && (
        <SendReceipt pid={pid} result={sent} asset={asset} onDismiss={() => setSent(null)} />
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

      {/* The same projection the back office reads, framed for the person whose
          money it is: no contra accounts, no double entry, no reconciliation
          check. The statement is theirs; the bookkeeping behind it is the
          bank's. It failing is its own failure and not the page's — the
          account above it resolved — so the retry offered here is the
          statement's, and the balances stay on screen. */}
      <div className="space-y-4">
        <h2 className="text-lg font-semibold tracking-tight">Activity</h2>
        {statement.error ? (
          <ErrorState error={statement.error} onRetry={() => statement.refetch()} />
        ) : statement.isLoading ? (
          <Skeleton className="h-48 w-full" />
        ) : (
          <>
            <StatementTable
              retail
              rows={statement.rows}
              book={statement.book}
              account={account.controlAccount}
              subsidiary={did}
              pid={pid}
              asset={asset}
            />
            <p className="text-xs text-muted-foreground">
              Money held for a card authorisation is not here — it reduces what
              you can spend without moving your balance, and only appears once it
              is taken.
            </p>
          </>
        )}
      </div>
    </div>
  );
}
