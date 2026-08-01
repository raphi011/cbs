"use client";

import { useParams } from "next/navigation";

import { useParticipant } from "@/lib/api/hooks";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";

// The back office of one member bank. Its sections are the shell's sidebar now,
// not tabs inside a page: a bank is a place you are, not a section of a network
// app. This layout validates the pid and names the bank.
export default function BankLayout({ children }: { children: React.ReactNode }) {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error } = useParticipant(pid);

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
      {error ? <ErrorState error={error} /> : children}
    </div>
  );
}
