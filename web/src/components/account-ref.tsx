"use client";

import Link from "next/link";

import { IdText } from "@/components/id-text";
import { useAllAccounts } from "@/lib/api/hooks";

// Renders an account id as `id · name`, the id linking to the account's detail
// page. `label` (a curated role word like "suspense", or "this account") wins
// over the resolved chart-of-accounts name. `stopPropagation` keeps the link
// clickable inside a row that has its own onClick (e.g. the statement table).
export function AccountRef({
  pid,
  id,
  label,
  className,
}: {
  pid: string;
  id: string;
  label?: string;
  className?: string;
}) {
  const { data } = useAllAccounts(pid);
  const name = label ?? data?.find((a) => a.id === id)?.name;
  return (
    <span className="inline-flex items-center gap-1.5">
      <Link
        href={`/bank/${pid}/accounts/${id}`}
        className="hover:underline"
        onClick={(e) => e.stopPropagation()}
      >
        <IdText id={id} className={className} />
      </Link>
      {name && <span className="text-xs text-muted-foreground">· {name}</span>}
    </span>
  );
}
