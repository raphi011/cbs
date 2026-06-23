"use client";

import { Combobox } from "@/components/ui/combobox";
import { AccountTypeBadge } from "@/components/enum-badge";
import { useAllAccounts } from "@/lib/api/hooks";

// Pick a general-ledger account within a participant. Searchable by account
// name, id, type, or its ledger/subledger names.
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
    detail: a.id,
    keywords: [a.type, a.subledgerName, a.ledgerName],
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
