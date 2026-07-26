"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";

import { useParticipant } from "@/lib/api/hooks";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

// Layout for the participant-scoped section. Validates the pid via
// GET /participants/{pid} (a 404 surfaces a friendly "not found"), shows the
// bank header, and renders sub-nav tabs. The tab list grows as later milestones
// add the ledger and deposit screens.
export default function ParticipantLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const pathname = usePathname();
  const { data, isLoading, error } = useParticipant(pid);

  const base = `/participants/${pid}`;
  const tabs: { href: string; label: string; exact?: boolean }[] = [
    { href: base, label: "Overview", exact: true },
    { href: `${base}/ledger`, label: "General ledger" },
    { href: `${base}/transactions`, label: "Transactions" },
    { href: `${base}/audit`, label: "Audit" },
    { href: `${base}/deposit-accounts`, label: "Deposit accounts" },
    { href: `${base}/deposit-audit`, label: "Deposit audit" },
  ];

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        {isLoading ? (
          <Skeleton className="h-8 w-48" />
        ) : (
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">
              {data?.name ?? "Participant"}
            </h1>
            {data && <IdText id={data.id} />}
          </div>
        )}
        <p className="text-sm text-muted-foreground">Member bank</p>
      </div>

      {error ? (
        <ErrorState error={error} />
      ) : (
        <>
          <nav className="flex gap-1 border-b">
            {tabs.map((t) => {
              const active = t.exact
                ? pathname === t.href
                : pathname.startsWith(t.href);
              return (
                <Link
                  key={t.href}
                  href={t.href}
                  className={cn(
                    "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
                    active
                      ? "border-foreground text-foreground"
                      : "border-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t.label}
                </Link>
              );
            })}
          </nav>
          {children}
        </>
      )}
    </div>
  );
}
