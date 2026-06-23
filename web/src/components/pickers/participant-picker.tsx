"use client";

import { Combobox } from "@/components/ui/combobox";
import { useParticipants } from "@/lib/api/hooks";

// Pick a participant (member bank) by name; the opaque id is what's emitted.
export function ParticipantPicker({
  value,
  onChange,
  id,
}: {
  value: string;
  onChange: (value: string) => void;
  id?: string;
}) {
  const { data, isLoading } = useParticipants();
  const options = (data ?? []).map((p) => ({
    value: p.id,
    label: p.name,
    detail: p.id,
  }));
  return (
    <Combobox
      id={id}
      options={options}
      value={value}
      onChange={onChange}
      loading={isLoading}
      placeholder="Select participant…"
      searchPlaceholder="Search participants…"
      emptyText="No participants."
    />
  );
}
