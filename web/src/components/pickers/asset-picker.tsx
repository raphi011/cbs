"use client";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAssets } from "@/lib/api/hooks";

// Picks the asset an account will be denominated in.
//
// A select rather than a text box, for the same reason every other ID entry in
// this app goes through a picker: the set is known (GET /assets — the
// definitions are compiled into the backend, not registered at runtime), and a
// free-text field lets a user type a code that will only be refused after the
// round trip. It also puts the scale on screen next to the code, which is the
// one number that decides what a typed amount means.
//
// `value` is the asset code, or "" for none chosen yet. There is no default:
// picking a currency on the caller's behalf is the bug the asset dimension
// exists to prevent.
export function AssetPicker({
  value,
  onChange,
  id,
  disabled,
}: {
  value: string;
  onChange: (code: string) => void;
  id?: string;
  disabled?: boolean;
}) {
  const assets = useAssets();
  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue placeholder={assets.isLoading ? "Loading…" : "Choose an asset"} />
      </SelectTrigger>
      <SelectContent>
        {(assets.data ?? []).map((a) => (
          <SelectItem key={a.code} value={a.code}>
            <span className="font-medium">{a.code}</span>
            <span className="text-muted-foreground">
              {a.name} · {a.scale} dp
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
