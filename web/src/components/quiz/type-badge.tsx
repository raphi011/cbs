import { cn } from "@/lib/utils";
import type { QuestionKind } from "@/lib/quiz/types";

const LABEL: Record<QuestionKind, string> = {
  mc: "Multiple choice",
  truefalse: "True / False",
  multi: "Select all",
  numeric: "Numeric",
};

/**
 * How a question is answered, said once and quietly.
 *
 * It carries no colour of its own. Four hues here taught a reader nothing —
 * the label already distinguishes the kinds, and the options below say it
 * again — while spending the only saturated ink on the card on a fact that is
 * never the point. The one thing on this surface that earns colour is whether
 * an answer was right.
 */
export function TypeBadge({ kind, className }: { kind: QuestionKind; className?: string }) {
  return (
    <span
      className={cn(
        // Outlined rather than filled: muted text on `--muted` measures 4.34:1
        // and fails AA, while the same text on the card behind it is 4.74:1.
        // A neutral pill has no fill that earns the contrast it costs.
        "inline-flex h-5 items-center rounded-full border px-2 text-xs font-medium text-muted-foreground",
        className,
      )}
    >
      {LABEL[kind]}
    </span>
  );
}
