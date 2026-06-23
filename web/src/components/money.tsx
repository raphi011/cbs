"use client";

import { useId, useState } from "react";

import { Input } from "@/components/ui/input";
import { centsToInput, formatCents, formatSigned, parseDollars } from "@/lib/money";
import { cn } from "@/lib/utils";

// Money renders integer cents as currency. `signed` adds an explicit +/- and is
// used for net positions and deltas.
export function Money({
  cents,
  signed = false,
  className,
}: {
  cents: number;
  signed?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("tabular-nums", className)}>
      {signed ? formatSigned(cents) : formatCents(cents)}
    </span>
  );
}

// AmountCell is a right-aligned, sign-colored cell for tables.
export function AmountCell({
  cents,
  signed = false,
}: {
  cents: number;
  signed?: boolean;
}) {
  const tone =
    cents > 0
      ? "text-emerald-700 dark:text-emerald-400"
      : cents < 0
        ? "text-red-700 dark:text-red-400"
        : "text-foreground";
  return (
    <span className={cn("block text-right tabular-nums", signed && tone)}>
      {signed ? formatSigned(cents) : formatCents(cents)}
    </span>
  );
}

interface MoneyInputProps {
  // Current value in integer cents, or null when empty.
  valueCents: number | null;
  onChangeCents: (cents: number | null) => void;
  id?: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
}

// MoneyInput edits major units (what people type, e.g. "30.00") but emits
// integer cents — the source of truth the API expects. It keeps its own text
// state so intermediate values like "30." are typeable.
export function MoneyInput({
  valueCents,
  onChangeCents,
  id,
  placeholder = "0.00",
  disabled,
  required,
}: MoneyInputProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const [text, setText] = useState(
    valueCents == null ? "" : centsToInput(valueCents),
  );

  function handleChange(next: string) {
    setText(next);
    onChangeCents(parseDollars(next));
  }

  return (
    <div className="relative">
      <span className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-sm text-muted-foreground">
        €
      </span>
      <Input
        id={inputId}
        inputMode="decimal"
        placeholder={placeholder}
        value={text}
        disabled={disabled}
        required={required}
        onChange={(e) => handleChange(e.target.value)}
        // Normalize to two decimals on blur when the value is valid.
        onBlur={() => {
          const cents = parseDollars(text);
          if (cents != null) setText(centsToInput(cents));
        }}
        className="pl-7 text-right tabular-nums"
      />
    </div>
  );
}
