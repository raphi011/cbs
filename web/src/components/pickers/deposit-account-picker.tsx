"use client";

import { Combobox } from "@/components/ui/combobox";
import { EnumBadge } from "@/components/enum-badge";
import { useDepositAccounts } from "@/lib/api/hooks";

// Pick a deposit account within a participant. Disabled until a participant is
// chosen (the list is scoped to that participant).
export function DepositAccountPicker({
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
  const { data, isLoading } = useDepositAccounts(pid);
  const options = (data ?? []).map((a) => ({
    value: a.id,
    label: a.name,
    detail: a.id,
    keywords: [a.status],
    badge: <EnumBadge value={a.status} />,
  }));
  return (
    <Combobox
      id={id}
      options={options}
      value={value}
      onChange={onChange}
      loading={pid !== "" && isLoading}
      disabled={pid === ""}
      placeholder={pid ? "Select account…" : "Choose a participant first"}
      searchPlaceholder="Search accounts…"
      emptyText="No deposit accounts for this participant."
    />
  );
}
