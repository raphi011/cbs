"use client";

import Link from "next/link";
import { Building2, GraduationCap, Landmark, Network } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Money, UnresolvedAmount } from "@/components/money";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { useAssetLookup, useIdentityDirectory, useReserves } from "@/lib/api/hooks";
import { homeFor } from "@/lib/identity";
import type { DepositAccount, Participant, Reserve } from "@/lib/types";

// The lobby. `/` never redirects: a first-time visitor is shown the cast and
// picks one. Remembering the last identity would save a repeat visitor a click
// and cost the newcomer the one screen that makes the app's structure obvious,
// which for a teaching system is the wrong trade — so nothing is persisted.
export default function Lobby() {
  const { banks, isLoading, error } = useIdentityDirectory();
  const { data: reserves } = useReserves();

  if (error) return <ErrorState error={error} />;

  return (
    <div className="mx-auto max-w-4xl space-y-8 py-4">
      <div className="space-y-2">
        <h1 className="text-3xl font-semibold tracking-tight">Who are you today?</h1>
        <p className="max-w-prose text-sm text-muted-foreground">
          There is no observer who sees all of this. A back office sees one
          bank&apos;s customers, a customer sees one account, the clearing house
          sees payments and the central bank sees reserves. Each of them talks to
          a different listener. Pick a seat.
        </p>
      </div>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Institutions</h2>
        <div className="grid gap-4 sm:grid-cols-2">
          <InstitutionCard
            href={homeFor({ persona: "central-bank" })}
            icon={<Landmark className="size-4" />}
            title="Central bank"
            hint="central-bank-reserves"
            body="Reserves, and settling a closed cycle by moving them. It never sees an individual payment."
          />
          <InstitutionCard
            href={homeFor({ persona: "clearing-house" })}
            icon={<Network className="size-4" />}
            title="Clearing house"
            hint="clearing-vs-settlement"
            body="Every payment in the network, the clearing cycles, the schemes, the mandates and the directory."
          />
        </div>
      </section>

      <section className="space-y-3">
        {/* Every bank the network lists, which is not the same as every MEMBER:
            a bank founded and not yet admitted is in here too. The card below says
            which, so the heading says the wider of the two truths. */}
        <h2 className="text-sm font-medium text-muted-foreground">Banks</h2>
        {isLoading && banks.length === 0 ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <Skeleton className="h-24" />
            <Skeleton className="h-24" />
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            {banks.map(({ participant, provisioned }) => (
              <BankCard
                key={participant.id}
                participant={participant}
                provisioned={provisioned}
                reserves={(reserves ?? []).filter((r) => r.participant === participant.id)}
              />
            ))}
          </div>
        )}
      </section>

      <section className="space-y-3">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Customers
          <Hint id="account-addressing" />
        </h2>
        <p className="text-sm text-muted-foreground">
          A customer identity is one deposit account — there is no party master
          here, so a second account would be a second identity.
        </p>
        {banks
          .filter((b) => b.provisioned)
          .map(({ participant, accounts }) => (
            <div key={participant.id} className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">{participant.name}</p>
              <div className="divide-y rounded-lg border">
                {accounts.length === 0 ? (
                  <p className="px-3 py-2.5 text-sm text-muted-foreground">
                    No customer accounts yet.
                  </p>
                ) : (
                  accounts.map((account) => (
                    <CustomerRow key={account.id} pid={participant.id} account={account} />
                  ))
                )}
              </div>
            </div>
          ))}
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Or just read</h2>
        <Link href="/learn">
          <Card className="transition-colors hover:border-foreground/30">
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <GraduationCap className="size-4" />
                Learn
              </CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-sm text-muted-foreground">
                Eighteen chapters, from double-entry bookkeeping to arrears.
              </p>
            </CardContent>
          </Card>
        </Link>
      </section>
    </div>
  );
}

function InstitutionCard({
  href,
  icon,
  title,
  hint,
  body,
}: {
  href: string;
  icon: React.ReactNode;
  title: string;
  hint: "central-bank-reserves" | "clearing-vs-settlement";
  body: string;
}) {
  return (
    <Link href={href}>
      <Card className="h-full transition-colors hover:border-foreground/30">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            {icon}
            {title}
            <Hint id={hint} />
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{body}</p>
        </CardContent>
      </Card>
    </Link>
  );
}

// A bank founded at runtime has a store row and a book of its own and no
// listener until the server restarts — joining a network is an operational act,
// and modelling it as an API call that instantly yields a running bank teaches
// the wrong thing. It is shown and not offered: a console whose every request
// 502s is worse than a sentence.
function BankCard({
  participant,
  provisioned,
  reserves,
}: {
  participant: Participant;
  provisioned: boolean;
  reserves: Reserve[];
}) {
  const { byCode, isLoading: assetLoading } = useAssetLookup();

  const body = (
    <Card
      className={
        provisioned ? "h-full transition-colors hover:border-foreground/30" : "h-full opacity-70"
      }
    >
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Building2 className="size-4" />
          {participant.name}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {provisioned ? (
          <>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Reserves
              <Hint id="reserve-account" />
            </p>
            {reserves.length === 0 ? (
              <p className="text-sm text-muted-foreground">None yet.</p>
            ) : (
              reserves.map((r) => {
                const asset = byCode.get(r.asset);
                return asset ? (
                  <p key={r.asset} className="text-lg font-semibold">
                    <Money amount={r.reserve} asset={asset} />
                  </p>
                ) : (
                  <UnresolvedAmount key={r.asset} code={r.asset} isLoading={assetLoading} />
                );
              })
            )}
          </>
        ) : (
          <p className="text-sm text-muted-foreground">
            <span className="font-medium text-foreground">Awaiting provisioning.</span> It was
            founded and the clearing house lists it, but it has no listener of its own until
            the server restarts.
          </p>
        )}
      </CardContent>
    </Card>
  );

  return provisioned ? (
    <Link href={homeFor({ persona: "bank", pid: participant.id })}>{body}</Link>
  ) : (
    body
  );
}

// Frozen and Closed accounts are listed and selectable on purpose: seeing the
// customer view of a frozen account is one of the better lessons here.
function CustomerRow({ pid, account }: { pid: string; account: DepositAccount }) {
  const iban = account.identifiers.find((i) => i.scheme === "IBAN");
  return (
    <Link
      href={homeFor({ persona: "customer", pid, did: account.id })}
      className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-sm font-medium">{account.name}</span>
        <EnumBadge value={account.status} />
      </span>
      {iban && <span className="font-mono text-xs text-muted-foreground">{iban.value}</span>}
    </Link>
  );
}
