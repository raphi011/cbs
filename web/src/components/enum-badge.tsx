import {
  ACCOUNT_TYPE_TONE,
  STATUS_TONE,
  type AccountType,
  type Tone,
} from "@/lib/enums";
import { cn } from "@/lib/utils";

const TONE_CLASS: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground",
  good: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  warn: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  bad: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  info: "bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300",
};

function Pill({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
        TONE_CLASS[tone],
      )}
    >
      {children}
    </span>
  );
}

// EnumBadge colors a status string (transaction/account/hold/payment/mandate/
// cycle) by its semantic tone. Unknown values fall back to neutral.
export function EnumBadge({ value }: { value: string }) {
  return <Pill tone={STATUS_TONE[value] ?? "neutral"}>{value}</Pill>;
}

// AccountTypeBadge colors one of the five chart-of-accounts types.
export function AccountTypeBadge({ type }: { type: AccountType }) {
  return <Pill tone={ACCOUNT_TYPE_TONE[type]}>{type}</Pill>;
}

// DirectionBadge: debit vs credit, neutral styling with mono emphasis.
export function DirectionBadge({ direction }: { direction: string }) {
  return <Pill tone={direction === "Debit" ? "info" : "good"}>{direction}</Pill>;
}
