"use client";

import { useParams } from "next/navigation";
import { RefreshCw } from "lucide-react";
import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { DataTable, type Column } from "@/components/data-table";
import { IdText } from "@/components/id-text";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/error-state";
import {
  useParticipants,
  useRefreshRoutingDirectory,
  useRoutingDirectory,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { RoutingEntry } from "@/lib/types";

// THIS BANK's copy of the scheme's routing directory, and the button that pulls
// a fresh one.
//
// It is the other end of the clearing house's screen of the same name, and the
// difference between the two is the whole subscription model. That one is the
// publication; this one is a snapshot somebody took of it, held in this bank's
// own database, and used to answer every payment this bank submits.
//
// The refresh is a request this bank makes. Nothing pushes at it, nothing polls
// on a timer, and the clearing house holds no list of who is listening — a
// publisher that knew its subscribers would be a delivery system instead. So the
// copy is exactly as current as the last time somebody pressed this.
//
// What that costs is the point rather than a caveat, and this screen is where it
// is visible: admit a bank on the central bank's console, come back here without
// refreshing, and a payment to that bank's customer is refused. One press makes
// the same payment work.

// How long ago the snapshot was taken, in the words a directory vendor would
// use. Every row of one pull carries the same instant — a snapshot is one act —
// so this reads any of them.
function sinceLabel(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.floor(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

function RefreshButton({ pid }: { pid: string }) {
  const refresh = useRefreshRoutingDirectory(pid);
  return (
    <Button
      size="sm"
      disabled={refresh.isPending}
      onClick={() =>
        refresh.mutate(undefined, {
          onSuccess: (entries) =>
            toast.success(
              `Directory refreshed — ${entries.length} ${entries.length === 1 ? "entry" : "entries"}`,
            ),
          onError: (err) => toast.error(describeError(err)),
        })
      }
    >
      <RefreshCw className="size-4" />
      {refresh.isPending ? "Pulling…" : "Refresh directory"}
    </Button>
  );
}

export default function BankDirectoryPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useRoutingDirectory(pid);
  const { data: participants } = useParticipants();

  // The copy carries no name, because the roster it was copied from carries
  // none, because the acknowledgement that wrote the roster delivers none. What
  // this column shows comes from a different table answering a different
  // question, and it is a courtesy of the console rather than something the
  // directory knows — which is exactly why a payer's send form cannot show one.
  const nameFor = (bic: string) =>
    participants?.find((p) => p.bic === bic)?.name ?? "—";

  const columns: Column<RoutingEntry>[] = [
    {
      key: "allocation",
      header: "Bank code",
      render: (e) => <IdText id={`${e.country} ${e.bankCode}`} />,
    },
    { key: "bic", header: "Routes to", render: (e) => <IdText id={e.bic} /> },
    { key: "member", header: "Member", render: (e) => nameFor(e.bic) },
    {
      key: "refreshedAt",
      header: "Pulled",
      render: (e) => sinceLabel(e.refreshedAt),
    },
  ];

  const entries = data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Routing directory (this bank's copy)"
        hint="routing-directory"
        description="Which institution answers for each bank code, as of the last time this bank pulled a snapshot. Every payment this bank submits reads an address's bank code out of this table — so a bank admitted since the last pull cannot be paid until the next one."
        actions={<RefreshButton pid={pid} />}
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <>
          <DataTable
            columns={columns}
            rows={entries}
            rowKey={(e) => `${e.country}/${e.bankCode}`}
            isLoading={isLoading}
            empty="This bank has never pulled the directory. Until it does, it can route nothing — being in the scheme and holding a copy of the scheme's directory are two separate acts."
          />
          {entries.length > 0 && (
            <p className="max-w-prose text-xs text-muted-foreground">
              {entries.length} {entries.length === 1 ? "entry" : "entries"},
              pulled {sinceLabel(entries[0].refreshedAt)}. A copy that is behind
              is <em>incomplete</em> and never <em>wrong</em>: a bank code is
              never reassigned, so the worst this table can say is &ldquo;I
              cannot route that yet&rdquo;. It never sends money to the wrong
              institution.
            </p>
          )}
        </>
      )}
    </div>
  );
}
