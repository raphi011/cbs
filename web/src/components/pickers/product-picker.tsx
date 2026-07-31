"use client";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useProducts } from "@/lib/api/hooks";

// Picks the catalogue product an account is opened FROM.
//
// A select rather than a text box, for the reason AssetPicker is one: the set
// is known (GET /participants/{pid}/products), and a free-text field lets a
// user type an ID that will only be refused after the round trip.
//
// RETIRED products are filtered out. A retired product is off sale — opening
// from one is refused with a 422 — but it is NOT unpriced: the accounts already
// sold from it keep resolving against its versions for as long as they live, so
// the listing endpoint returns them and it is this form, not the API, that
// declines to offer them.
//
// The product carries the PRICE and this form does not ask for one; the
// overdraft limit is asked for and the product cannot express one. That is the
// pinned/floating distinction made visible: a rate is what the bank charges for
// a product, a limit is an underwriting decision about this customer.
export function ProductPicker({
  pid,
  value,
  onChange,
  id,
  disabled,
}: {
  pid: string;
  value: string;
  onChange: (id: string) => void;
  id?: string;
  disabled?: boolean;
}) {
  const products = useProducts(pid);
  const onSale = (products.data ?? []).filter((p) => !p.retired);

  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue
          placeholder={products.isLoading ? "Loading…" : "Choose a product"}
        />
      </SelectTrigger>
      <SelectContent>
        {onSale.map((p) => (
          <SelectItem key={p.id} value={p.id}>
            <span className="font-medium">{p.name}</span>
            <span className="text-muted-foreground">{p.kind}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
