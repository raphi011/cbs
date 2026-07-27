"use client";

import { useEffect, useId, useState } from "react";

import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  amountToInput,
  formatAmount,
  formatSigned,
  parseAmount,
  type AssetScale,
} from "@/lib/money";
import { cn } from "@/lib/utils";

// Money renders an integer amount at its asset's scale, with the asset code
// alongside it — an amount is meaningless without knowing which asset it's
// denominated in, and now that a network can hold more than one, the code
// is part of the number, not decoration. `signed` adds an explicit +/- and is
// used for net positions and deltas.
export function Money({
  amount,
  asset,
  signed = false,
  className,
}: {
  amount: number;
  asset: AssetScale;
  signed?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("tabular-nums", className)}>
      {signed ? formatSigned(amount, asset) : formatAmount(amount, asset)}
      <span className="ml-1 text-xs font-normal text-muted-foreground">
        {asset.code}
      </span>
    </span>
  );
}

// UnresolvedAmount is what goes on screen in place of an amount whose asset has
// not resolved to a definition — the one thing that must NOT happen there is a
// number, because rendering minor units at a guessed scale is the bug this
// whole dimension exists to prevent.
//
// It exists so the choice is made once. Three different answers had grown up
// across the app: an indefinite skeleton (which claims "still loading" forever
// when the code will never resolve), a bare `null` (which silently drops a row),
// and this. Loading is a skeleton; anything else is an explicit dash that says
// what went wrong on hover.
export function UnresolvedAmount({
  code,
  isLoading,
  className,
}: {
  code: string;
  isLoading?: boolean;
  className?: string;
}) {
  if (isLoading) return <Skeleton className={cn("h-4 w-16", className)} />;
  return (
    <span
      className={cn("text-muted-foreground", className)}
      title={`No definition for asset "${code}" — amount not rendered rather than shown at a guessed scale.`}
    >
      —
    </span>
  );
}

// AmountCell is a right-aligned, sign-colored cell for tables.
export function AmountCell({
  amount,
  asset,
  signed = false,
}: {
  amount: number;
  asset: AssetScale;
  signed?: boolean;
}) {
  const tone =
    amount > 0
      ? "text-emerald-700 dark:text-emerald-400"
      : amount < 0
        ? "text-red-700 dark:text-red-400"
        : "text-foreground";
  return (
    <span className={cn("block text-right tabular-nums", signed && tone)}>
      {signed ? formatSigned(amount, asset) : formatAmount(amount, asset)}
      <span className="ml-1 text-xs font-normal text-muted-foreground">
        {asset.code}
      </span>
    </span>
  );
}

interface MoneyInputProps {
  // Current value in the asset's integer minor units, or null when empty.
  value: number | null;
  onChange: (amount: number | null) => void;
  asset: AssetScale;
  id?: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
}

// MoneyInput edits major units (what people type, e.g. "30.00", or for an
// 8-decimal asset "0.00000001") but emits an integer amount at the asset's
// scale — the source of truth the API expects. It keeps its own text state so
// intermediate values like "30." are typeable.
export function MoneyInput({
  value,
  onChange,
  asset,
  id,
  placeholder,
  disabled,
  required,
}: MoneyInputProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const [text, setText] = useState(
    value == null ? "" : amountToInput(value, asset),
  );

  // Resync the displayed text when the asset changes, and when a parent clears
  // `value` to null.
  //
  // `text` is otherwise decoupled from `value` after mount — deliberately, so
  // an in-progress keystroke like "30." isn't clobbered by re-formatting on
  // every render — but that same decoupling leaves stale text on screen in two
  // cases. One is a scale change: minor units typed at one scale, reinterpreted
  // unchanged as another's. The other is a parent resetting the value out from
  // under the input, which happens on every account switch in
  // post-transaction-form; keying on the asset alone missed it whenever the two
  // accounts shared an asset, leaving a filled-looking box, `ready` false and
  // Post disabled with nothing on screen to explain it.
  //
  // They are two effects because the null case must only ever blank the box,
  // never reformat it. A single effect keyed on `value` would reformat "3" to
  // "3.00" on the keystroke that produced it.
  useEffect(() => {
    /* eslint-disable-next-line react-hooks/set-state-in-effect */
    setText(value == null ? "" : amountToInput(value, asset));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [asset.code, asset.scale]);

  useEffect(() => {
    /* eslint-disable-next-line react-hooks/set-state-in-effect */
    if (value == null) setText("");
  }, [value]);

  function handleChange(next: string) {
    setText(next);
    onChange(parseAmount(next, asset));
  }

  return (
    <div className="relative">
      <span className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-xs text-muted-foreground">
        {asset.code}
      </span>
      <Input
        id={inputId}
        inputMode="decimal"
        placeholder={placeholder ?? (asset.scale > 0 ? (0).toFixed(asset.scale) : "0")}
        value={text}
        disabled={disabled}
        required={required}
        onChange={(e) => handleChange(e.target.value)}
        // Normalize to the asset's scale on blur when the value is valid.
        onBlur={() => {
          const amount = parseAmount(text, asset);
          if (amount != null) setText(amountToInput(amount, asset));
        }}
        className="pl-12 text-right tabular-nums"
      />
    </div>
  );
}
