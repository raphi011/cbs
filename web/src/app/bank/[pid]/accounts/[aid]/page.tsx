"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ArrowLeft } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { IdText } from "@/components/id-text";
import { Money } from "@/components/money";
import { AccountTypeBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { StatementTable } from "@/components/statement/statement-table";
import {
  useAccountStatement,
  useAssetLookup,
  useDepositAccounts,
  useFacilities,
  useGLAccount,
  useParticipant,
  useSubsidiaries,
} from "@/lib/api/hooks";
import { buildKnownAccounts } from "@/lib/statement";

export default function AccountDetailPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const aid = typeof params.aid === "string" ? params.aid : "";

  const { account, isLoading: accLoading, error: accError, refetch } = useGLAccount(pid, aid);
  const statement = useAccountStatement(pid, aid, account?.type);
  const { data: deposits } = useDepositAccounts(pid);
  const { data: facilities } = useFacilities(pid);
  const { data: participant } = useParticipant(pid);
  const { data: subsidiaries } = useSubsidiaries(pid, aid);
  const { byCode, isLoading: assetsLoading } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;

  const back = `/bank/${pid}/ledger`;
  const role = participant ? buildKnownAccounts(participant)[aid] : undefined;

  // What a subsidiary IS, the ledger does not know: it holds a string the layer
  // above supplied. This page is that layer, so it resolves the string against
  // the two things it can be — a deposit account or a facility — and shows the
  // raw id when it is neither, rather than pretending it resolved.
  const subsidiaryRef = (id: string) => {
    const deposit = deposits?.find((d) => d.id === id);
    if (deposit) return { name: deposit.name, href: `/bank/${pid}/deposit-accounts/${deposit.id}` };
    const facility = facilities?.find((f) => f.id === id);
    if (facility) return { name: facility.name, href: `/bank/${pid}/facilities/${facility.id}` };
    return undefined;
  };

  return (
    <div className="space-y-5">
      <Link href={back} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> Chart of accounts
      </Link>

      {accError ? (
        <ErrorState error={accError} onRetry={() => refetch()} />
      ) : accLoading ? (
        <Skeleton className="h-10 w-64" />
      ) : !account ? (
        <ErrorState error={new Error(`Account ${aid} not found in the chart of accounts.`)} onRetry={() => refetch()} />
      ) : assetsLoading ? (
        <Skeleton className="h-10 w-64" />
      ) : !asset ? (
        <ErrorState
          error={
            new Error(
              `This account is denominated in "${account.asset}", which the system has no definition for, so its amounts cannot be rendered at a known scale.`,
            )
          }
          onRetry={() => refetch()}
        />
      ) : (
        <>
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">{account.name}</h2>
              <AccountTypeBadge type={account.type} />
              <IdText id={account.id} />
            </div>
            <p className="text-sm text-muted-foreground">
              {account.ledgerName} · {account.subledgerName}
              {role && (
                <>
                  {" · "}the participant&apos;s <span className="font-medium">{role}</span> account
                </>
              )}
            </p>
            {account.control && (
              <p className="text-sm text-muted-foreground">
                A control account: one line standing for many, with every posting
                against it naming whose money it is.
              </p>
            )}
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">Balance</CardTitle>
            </CardHeader>
            <CardContent>
              {statement.book == null ? (
                <Skeleton className="h-6 w-24" />
              ) : (
                <div className="text-lg font-semibold tabular-nums">
                  <Money amount={statement.book} asset={asset} />
                </div>
              )}
            </CardContent>
          </Card>

          {account.control && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Who this line stands for</CardTitle>
              </CardHeader>
              <CardContent>
                {subsidiaries == null ? (
                  <Skeleton className="h-16 w-full" />
                ) : subsidiaries.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    Nobody owes anything under this line today. The balance above is the sum of
                    the rows here, so an empty list and a zero balance are the same statement.
                  </p>
                ) : (
                  <ul className="divide-y text-sm">
                    {subsidiaries.map((row) => {
                      const who = subsidiaryRef(row.subsidiary);
                      return (
                        <li key={row.subsidiary} className="flex items-center justify-between py-2">
                          {who ? (
                            <Link href={who.href} className="underline hover:text-foreground">
                              {who.name}
                            </Link>
                          ) : (
                            <IdText id={row.subsidiary} />
                          )}
                          <span className="tabular-nums">
                            <Money amount={row.balance} asset={asset} />
                          </span>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </CardContent>
            </Card>
          )}

          <div className="space-y-2">
            <h3 className="text-sm font-medium text-muted-foreground">Account ledger</h3>
            {statement.error ? (
              <ErrorState error={statement.error} onRetry={() => statement.refetch()} />
            ) : statement.isLoading ? (
              <Skeleton className="h-64 w-full" />
            ) : (
              <StatementTable
                rows={statement.rows}
                book={statement.book}
                account={aid}
                pid={pid}
                asset={asset}
                amountHintId="normal-balance"
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}
