"use client";

import { Combobox } from "@/components/ui/combobox";
import { AccountTypeBadge } from "@/components/enum-badge";
import { useAllAccounts } from "@/lib/api/hooks";

// Pick a general-ledger account within a participant. Searchable by account
// name, id, type, asset, or its ledger/subledger names.
//
// The asset is shown, not just searchable. In a multi-asset chart of accounts
// "Cash (EUR)" and "Cash (BTC)" are two different accounts whose labels differ
// only there, and a transaction whose legs land in different assets is refused
// by the server — so a picker that hides the asset is what lets a user build
// that transaction in the first place.
export function GLAccountPicker({
  pid,
  value,
  onChange,
  id,
}: {
  pid: string;
  value: string;
  onChange: (value: string) => void;
  id?: string;
}) {
  const { data, isLoading } = useAllAccounts(pid);
  const options = (data ?? []).map((a) => ({
    value: a.id,
    label: a.name,
    detail: `${a.asset} · ${a.id}`,
    keywords: [a.type, a.asset, a.subledgerName, a.ledgerName],
    badge: <AccountTypeBadge type={a.type} />,
  }));
  return (
    <Combobox
      id={id}
      options={options}
      value={value}
      onChange={onChange}
      loading={pid !== "" && isLoading}
      disabled={pid === ""}
      placeholder="Select GL account…"
      searchPlaceholder="Search by name, type, ledger…"
      emptyText="No accounts. Create some on the General ledger tab."
    />
  );
}
