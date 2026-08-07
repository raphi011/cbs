"use client";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent } from "@/components/ui/card";
import { DataTable } from "@/components/data-table";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { IdText } from "@/components/id-text";
import { useRoster, useParticipants } from "@/lib/api/hooks";
import type { RosterEntry } from "@/lib/types";

// The clearing house's ROUTING directory: every address the scheme will send a
// message to.
//
// It does NOT answer "type an IBAN, see which bank holds it". The two questions
// look alike and are not.
//
// That lookup is a SWEEP. The clearing house holds no deposit register of its
// own, so answering "who holds this IBAN" meant reading every member bank's
// register in turn, on an operator's screen. No institution in this network can
// do that: a bank holds its own register and no other, and there is no directory
// SERVICE here to hold the network-wide index. Real networks have one — SEPA
// banks subscribe to an IBAN-to-BIC table, and non-bank aliases go to a separate
// central service — and this system deliberately does not pretend to.
//
// What the clearing house genuinely owns is this: the roster, written from each
// admission's acknowledgement, keyed by BIC. It answers "where may a message
// addressed to this member go", which is the question a clearing house actually
// has to answer in order to route.
export default function DirectoryPage() {
  const { data, isLoading, error } = useRoster();
  const { data: participants } = useParticipants();

  // The roster carries no name — an acmt.010 has no name element — so the name
  // beside an address comes from GET /members, which is a different table
  // answering a different question. An address with no member row is rendered as
  // itself rather than as a blank.
  const nameFor = (bic: string) =>
    participants?.find((p) => p.bic === bic)?.name ?? "—";

  return (
    <div className="space-y-6">
      <PageHeader
        title="Routing directory"
        hint="routing-roster"
        description="Where a message addressed to a member may be sent. It is a list of addresses, not a register of accounts — the clearing house holds no book and no customer."
      />

      {error ? (
        <ErrorState error={error} />
      ) : isLoading ? (
        <Skeleton className="h-48 w-full" />
      ) : (
        <Card>
          <CardContent>
            <DataTable
              rows={data ?? []}
              rowKey={(e: RosterEntry) => e.bic}
              empty="No member has been admitted yet. A bank exists before it joins a scheme, and until it does there is nowhere to route to."
              columns={[
                {
                  key: "bic",
                  header: "Address",
                  render: (e: RosterEntry) => <IdText id={e.bic} />,
                },
                {
                  key: "member",
                  header: "Member",
                  render: (e: RosterEntry) => nameFor(e.bic),
                },
                {
                  key: "assets",
                  header: "Clears in",
                  render: (e: RosterEntry) => e.assets.join(", "),
                },
                {
                  key: "admissionRef",
                  header: "Admission",
                  render: (e: RosterEntry) => <IdText id={e.admissionRef} />,
                },
              ]}
            />
          </CardContent>
        </Card>
      )}

      <p className="max-w-prose text-xs text-muted-foreground">
        There is no address lookup here, and its absence is the teaching point.
        Asking &ldquo;whose IBAN is this?&rdquo; means reading somebody&apos;s
        deposit register, and the only institution entitled to read one is the
        bank that keeps it. A payer is told the payee&apos;s BIC the same way they
        are told the IBAN — off an invoice — and a payment sent to the wrong bank
        comes back refused rather than quietly re-routed.
      </p>
    </div>
  );
}
