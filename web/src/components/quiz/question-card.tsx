"use client";

import Link from "next/link";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConceptMarkdown } from "@/components/concept-markdown";
import { cn } from "@/lib/utils";
import { isCorrect, type Response, type SessionItem } from "@/lib/quiz/session";
import type { Question } from "@/lib/quiz/types";
import { TypeBadge } from "./type-badge";

const DIFF_LABEL = { intro: "Intro", core: "Core", challenge: "Challenge" } as const;

type OptionState = "idle" | "selected" | "correct" | "wrong";

const OPTION_CLASS: Record<OptionState, string> = {
  idle: "border-border bg-background hover:border-foreground/30",
  selected: "border-primary ring-1 ring-primary",
  correct: "border-emerald-500 bg-emerald-50 dark:bg-emerald-950/30",
  wrong: "border-destructive bg-destructive/5",
};

export function QuestionCard({
  item,
  response,
  phase,
  onResponse,
  onCheck,
  onNext,
  isLast,
}: {
  item: SessionItem;
  response: Response | null;
  phase: "answering" | "answered";
  onResponse: (r: Response) => void;
  onCheck: () => void;
  onNext: () => void;
  isLast: boolean;
}) {
  const q = item.question;
  const answered = phase === "answered";
  const correct = answered && isCorrect(q, response);

  const hasResponse =
    response != null &&
    (response.kind !== "multi" || response.choices.length > 0) &&
    (response.kind !== "numeric" || Number.isFinite(response.value));

  return (
    <Card className="p-6">
      <div className="flex items-center gap-2">
        <TypeBadge kind={q.kind} />
        {q.difficulty && (
          <span className="text-xs font-medium text-muted-foreground">
            {DIFF_LABEL[q.difficulty]}
          </span>
        )}
      </div>

      <h2 className="mt-3 text-lg font-semibold leading-snug">{q.prompt}</h2>

      <div className="mt-4 space-y-2">{renderInputs(q)}</div>

      {answered && (
        <div
          className={cn(
            "mt-4 rounded-lg border p-4",
            correct
              ? "border-emerald-500/40 bg-emerald-50 dark:bg-emerald-950/30"
              : "border-destructive/40 bg-destructive/5",
          )}
        >
          <div
            className={cn(
              "mb-1 text-sm font-bold",
              correct ? "text-emerald-600 dark:text-emerald-400" : "text-destructive",
            )}
          >
            {correct ? "✓ Correct" : "✗ Not quite"}
          </div>
          <ConceptMarkdown body={q.explanation} />
          {q.explore && (
            <Link
              href={q.explore.href}
              className="mt-1 inline-block text-sm font-medium text-primary underline underline-offset-2"
            >
              → {q.explore.label}
            </Link>
          )}
        </div>
      )}

      <div className="mt-5 flex justify-end">
        {answered ? (
          <Button onClick={onNext}>{isLast ? "See results →" : "Next question →"}</Button>
        ) : (
          <Button onClick={onCheck} disabled={!hasResponse}>
            Check answer →
          </Button>
        )}
      </div>
    </Card>
  );

  function renderInputs(question: Question) {
    if (question.kind === "mc") {
      return item.optionOrder.map((orig) => {
        const state: OptionState = !answered
          ? response?.kind === "mc" && response.choice === orig
            ? "selected"
            : "idle"
          : orig === question.answer
            ? "correct"
            : response?.kind === "mc" && response.choice === orig
              ? "wrong"
              : "idle";
        return (
          <button
            key={orig}
            type="button"
            disabled={answered}
            onClick={() => onResponse({ kind: "mc", choice: orig })}
            className={cn(
              "flex w-full items-center gap-3 rounded-lg border px-4 py-3 text-left text-sm transition-colors",
              OPTION_CLASS[state],
            )}
          >
            {question.options[orig]}
          </button>
        );
      });
    }

    if (question.kind === "truefalse") {
      return (["True", "False"] as const).map((label, i) => {
        const value = i === 0;
        const state: OptionState = !answered
          ? response?.kind === "truefalse" && response.choice === value
            ? "selected"
            : "idle"
          : question.answer === value
            ? "correct"
            : response?.kind === "truefalse" && response.choice === value
              ? "wrong"
              : "idle";
        return (
          <button
            key={label}
            type="button"
            disabled={answered}
            onClick={() => onResponse({ kind: "truefalse", choice: value })}
            className={cn(
              "flex w-full items-center gap-3 rounded-lg border px-4 py-3 text-left text-sm font-medium transition-colors",
              OPTION_CLASS[state],
            )}
          >
            {label}
          </button>
        );
      });
    }

    if (question.kind === "multi") {
      const chosen = response?.kind === "multi" ? response.choices : [];
      return item.optionOrder.map((orig) => {
        const isChosen = chosen.includes(orig);
        const state: OptionState = !answered
          ? isChosen
            ? "selected"
            : "idle"
          : question.answers.includes(orig)
            ? "correct"
            : isChosen
              ? "wrong"
              : "idle";
        return (
          <button
            key={orig}
            type="button"
            disabled={answered}
            onClick={() =>
              onResponse({
                kind: "multi",
                choices: isChosen ? chosen.filter((c) => c !== orig) : [...chosen, orig],
              })
            }
            className={cn(
              "flex w-full items-center gap-3 rounded-lg border px-4 py-3 text-left text-sm transition-colors",
              OPTION_CLASS[state],
            )}
          >
            <span
              className={cn(
                "grid size-4 place-items-center rounded border text-[10px]",
                isChosen ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground/40",
              )}
            >
              {isChosen ? "✓" : ""}
            </span>
            {question.options[orig]}
          </button>
        );
      });
    }

    // numeric
    const value =
      response?.kind === "numeric" && Number.isFinite(response.value) ? String(response.value) : "";
    return (
      <div className="flex items-center gap-2">
        <Input
          type="number"
          inputMode="decimal"
          disabled={answered}
          value={value}
          onChange={(e) =>
            onResponse({
              kind: "numeric",
              value: e.target.value === "" ? NaN : Number(e.target.value),
            })
          }
          className={cn(
            "max-w-40",
            answered && (correct ? "border-emerald-500" : "border-destructive"),
          )}
        />
        {question.unit && (
          <span className="text-sm text-muted-foreground">
            {question.unit === "dollars" ? "dollars" : "cents"}
          </span>
        )}
      </div>
    );
  }
}
