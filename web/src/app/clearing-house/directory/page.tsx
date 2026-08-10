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
// message to, and the allocation each member issues its customers' addresses
// under.
//
// It does NOT answer "type an IBAN, see which bank holds it". The two questions
// look alike and are not.
//
// That lookup is a SWEEP. The clearing house holds no deposit register of its
// own, so answering "who holds this IBAN" meant reading every member bank's
// register in turn, on an operator's screen. No institution in this network can
// do that: a bank holds its own register and no other, and nothing anywhere here
// holds a network-wide index of accounts.
//
// What it DOES hold is the pairing a payer's bank needs — country and bank code
// to BIC — and this screen is where it is PUBLISHED. Each member copies it into
// a directory of its own and routes from that copy; see
// /bank/[pid]/directory for the other end of the same table.
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
        description="Where a message addressed to a member may be sent, and which allocation that member issues its customers' addresses under. It is a list of addresses, not a register of accounts — the clearing house holds no book and no customer."
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
                  key: "allocation",
                  header: "Issues under",
                  render: (e: RosterEntry) => (
                    <IdText id={`${e.country} ${e.bankCode}`} />
                  ),
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
        This is a publication, not a service. Nothing pushes it at anybody and no
        bank asks it per payment: each member pulls a snapshot onto its own
        console and routes from that copy until it asks again, which is why a
        bank admitted this morning cannot be paid by a member that refreshed
        yesterday. There is no address lookup here either, and its absence is the
        teaching point — asking &ldquo;whose IBAN is this?&rdquo; means reading
        somebody&apos;s deposit register, and the only institution entitled to
        read one is the bank that keeps it.
      </p>
    </div>
  );
}
