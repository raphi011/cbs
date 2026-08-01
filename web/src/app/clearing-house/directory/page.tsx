"use client";

import { useState } from "react";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { IdText } from "@/components/id-text";
import { useCsmDirectory, useParticipants } from "@/lib/api/hooks";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { describeError } from "@/lib/api/errors";

// Type an address, see who holds it.
//
// Routing is by id and an IBAN is not one, so somebody has to answer "which
// bank?" before a payment can be built — and that is the clearing house's job.
// The register stops at the bank: a bank-issued identifier is globally unique by
// construction, and an address two banks both claim is refused rather than
// resolved to the first hit.
export default function DirectoryPage() {
  const [value, setValue] = useState("");
  const settled = useDebouncedValue(value.trim(), 350);
  const entry = useCsmDirectory(settled ? "IBAN" : "", settled);
  const { data: participants } = useParticipants();

  const bankName = (pid: string) => participants?.find((p) => p.id === pid)?.name ?? pid;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Directory"
        hint="account-addressing"
        description="An account's id is its bank's own key and never leaves it. An address is what a counterparty quotes — and resolving one to the account behind it is the network's question, not any single bank's."
      />

      <Card>
        <CardContent className="space-y-4">
          <div className="space-y-1.5">
            <FieldLabel htmlFor="directory-iban">IBAN</FieldLabel>
            <Input
              id="directory-iban"
              value={value}
              placeholder="SE89-AURORA-1001"
              className="font-mono"
              onChange={(e) => setValue(e.target.value)}
            />
          </div>

          {!settled ? (
            <p className="text-sm text-muted-foreground">
              Nothing typed yet. The seeded dataset addresses every customer
              account, so any of them resolves.
            </p>
          ) : entry.isLoading ? (
            <Skeleton className="h-16 w-full" />
          ) : entry.error ? (
            <p className="text-sm text-destructive">{describeError(entry.error)}</p>
          ) : entry.data ? (
            <dl className="grid gap-3 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs text-muted-foreground">Holder</dt>
                <dd className="font-medium">{entry.data.name}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Bank</dt>
                <dd className="font-medium">{bankName(entry.data.participant)}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Account</dt>
                <dd>
                  <IdText id={entry.data.account} />
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Asset</dt>
                <dd>{entry.data.asset}</dd>
              </div>
            </dl>
          ) : null}
        </CardContent>
      </Card>

      <p className="max-w-prose text-xs text-muted-foreground">
        The account id is shown because this is an operator&apos;s screen. It is
        the holding bank&apos;s own key and a counterparty never sees it — which
        is exactly why an address exists.
      </p>
    </div>
  );
}
