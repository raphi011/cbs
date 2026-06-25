import { cn } from "@/lib/utils";
import type { QuestionKind } from "@/lib/quiz/types";

const LABEL: Record<QuestionKind, string> = {
  mc: "Multiple choice",
  truefalse: "True / False",
  multi: "Select all",
  numeric: "Numeric",
};

const STYLE: Record<QuestionKind, string> = {
  mc: "bg-indigo-50 text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-300",
  truefalse: "bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300",
  multi: "bg-violet-50 text-violet-700 dark:bg-violet-950/40 dark:text-violet-300",
  numeric: "bg-teal-50 text-teal-700 dark:bg-teal-950/40 dark:text-teal-300",
};

export function TypeBadge({ kind, className }: { kind: QuestionKind; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center rounded-full px-2 text-xs font-semibold",
        STYLE[kind],
        className,
      )}
    >
      {LABEL[kind]}
    </span>
  );
}
