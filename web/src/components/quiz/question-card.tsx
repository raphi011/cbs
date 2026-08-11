"use client";

import { useEffect, useRef } from "react";
import Link from "next/link";
import { Check, Lightbulb, X } from "lucide-react";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConceptMarkdown } from "@/components/concept-markdown";
import { cn } from "@/lib/utils";
import { isCorrect, type Response, type SessionItem } from "@/lib/quiz/session";
import type { Question } from "@/lib/quiz/types";
import { unitLabel } from "@/lib/quiz/units";
import { TypeBadge } from "./type-badge";

const DIFF_LABEL = { intro: "Intro", core: "Core", challenge: "Challenge" } as const;

/** What each tier means, so the word beside a question is not a bare label. */
const DIFF_TITLE = {
  intro: "Intro — the chapter's starting idea",
  core: "Core — the chapter's main argument",
  challenge: "Challenge — the edge cases",
} as const;

const LETTERS = "ABCDEFGH";

type OptionState = "idle" | "selected" | "correct" | "wrong";

const OPTION_BASE =
  "relative flex w-full min-h-11 cursor-pointer items-center gap-3 rounded-lg border px-4 py-3 text-left text-sm transition-colors " +
  "has-[:focus-visible]:ring-3 has-[:focus-visible]:ring-ring/50 has-[:disabled]:cursor-default";

const OPTION_CLASS: Record<OptionState, string> = {
  // `--control-border` rather than `--border`: an option is a control, and a
  // hairline at 1.26:1 on white is not the 3:1 boundary WCAG 1.4.11 asks for.
  idle: "border-control-border bg-control hover:border-foreground/50",
  selected: "border-primary bg-control ring-1 ring-primary",
  correct: "border-success bg-success-muted",
  wrong: "border-destructive bg-destructive-muted",
};

/** The mark and the spoken text for a graded option — so colour is never alone. */
function grade(state: OptionState, chosen: boolean): { mark: "correct" | "wrong" | null; say: string } {
  if (state === "correct") {
    return chosen
      ? { mark: "correct", say: "Your answer. Correct." }
      : { mark: "correct", say: "Correct answer." };
  }
  if (state === "wrong") return { mark: "wrong", say: "Your answer. Incorrect." };
  return { mark: null, say: chosen ? "Your answer." : "" };
}

function OptionMark({ state, chosen }: { state: OptionState; chosen: boolean }) {
  const { mark, say } = grade(state, chosen);
  return (
    <>
      {mark === "correct" && (
        <Check aria-hidden="true" className="size-4 shrink-0 text-success-strong" />
      )}
      {mark === "wrong" && (
        <X aria-hidden="true" className="size-4 shrink-0 text-destructive-strong" />
      )}
      {say && <span className="sr-only">{say}</span>}
    </>
  );
}

export function QuestionCard({
  item,
  response,
  phase,
  conceptRevealed,
  onResponse,
  onRevealConcept,
  onCheck,
  onNext,
  isLast,
}: {
  item: SessionItem;
  response: Response | null;
  phase: "answering" | "answered";
  conceptRevealed: boolean;
  onResponse: (r: Response) => void;
  onRevealConcept: () => void;
  onCheck: () => void;
  onNext: () => void;
  isLast: boolean;
}) {
  const q = item.question;
  const answered = phase === "answered";
  const correct = answered && isCorrect(q, response);
  const promptId = `prompt-${q.id}`;
  const verdictRef = useRef<HTMLHeadingElement>(null);

  // Checking an answer injects the verdict and the explanation ABOVE the button
  // that was just pressed, whose label then changes to "Next question". Without
  // moving focus, a screen reader announces only the new label and never the
  // outcome — the entire teaching payload is silent. Focus goes to the verdict
  // heading instead, which reads the result and leaves the reader at the top of
  // the explanation. That is also why there is no `aria-live` here: the region
  // would announce the same text a second time.
  useEffect(() => {
    if (answered) verdictRef.current?.focus();
  }, [answered, q.id]);

  const hasResponse =
    response != null &&
    (response.kind !== "multi" || response.choices.length > 0) &&
    (response.kind !== "numeric" || Number.isFinite(response.value));

  return (
    // `overflow-visible` overrides Card's own `overflow-hidden`, which would
    // otherwise become the scroll container for the sticky action bar below and
    // stop it sticking to anything. The bar re-rounds its own bottom corners,
    // so the card silhouette survives losing the clip.
    <Card className="overflow-visible p-6">
      <div className="flex flex-wrap items-center gap-2">
        <TypeBadge kind={q.kind} />
        {q.difficulty && (
          <span
            className="text-xs font-medium text-muted-foreground"
            title={DIFF_TITLE[q.difficulty]}
          >
            {DIFF_LABEL[q.difficulty]}
          </span>
        )}
      </div>

      <h2 id={promptId} className="mt-3 text-lg font-semibold leading-snug">
        {q.prompt}
      </h2>

      {q.kind === "multi" && !answered && (
        <p className="mt-1 text-sm text-muted-foreground">Select all that apply.</p>
      )}

      <div className="mt-4">{renderInputs(q)}</div>

      {answered && (
        <div
          className={cn(
            "mt-4 rounded-lg border p-4",
            correct
              ? "border-success/40 bg-success-muted"
              : "border-destructive/40 bg-destructive-muted",
          )}
        >
          <h3
            ref={verdictRef}
            tabIndex={-1}
            className={cn(
              "mb-1 text-sm font-bold outline-none",
              correct ? "text-success-strong" : "text-destructive-strong",
            )}
          >
            {correct ? "Correct" : "Not quite"}
          </h3>
          {/* The explanation is the reason a reader is here, so it is rendered at
              full foreground weight rather than as muted secondary text. */}
          <ConceptMarkdown body={q.explanation} className="text-foreground" />
          {q.explore && (
            <Link
              href={q.explore.href}
              className="mt-1 inline-flex min-h-11 items-center text-sm font-medium text-primary underline underline-offset-2 sm:min-h-0"
            >
              → {q.explore.label}
            </Link>
          )}
        </div>
      )}

      {/* On a phone the card is taller than the viewport, so the only control
          that matters was consistently below the fold. It sticks to the bottom
          there and returns to the flow once there is room. */}
      <div
        className={cn(
          "sticky bottom-0 z-10 -mx-6 -mb-6 mt-5 flex items-center justify-between gap-2",
          "rounded-b-xl border-t bg-card px-6 py-3",
          "sm:static sm:m-0 sm:mt-5 sm:rounded-none sm:border-0 sm:bg-transparent sm:p-0",
        )}
      >
        <div className="min-w-0">
          {!answered && q.concept && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onRevealConcept}
              className="min-h-11 gap-1.5 text-muted-foreground sm:min-h-9"
            >
              <Lightbulb aria-hidden="true" className="size-3.5" />
              {conceptRevealed ? "Concept" : "Show the concept"}
            </Button>
          )}
        </div>
        {answered ? (
          <Button onClick={onNext} className="min-h-11 shrink-0">
            {isLast ? "See results" : "Next question"}
          </Button>
        ) : (
          <Button onClick={onCheck} disabled={!hasResponse} className="min-h-11 shrink-0">
            Check answer
          </Button>
        )}
      </div>
    </Card>
  );

  function renderInputs(question: Question) {
    if (question.kind === "mc") {
      return (
        // A real radio group: grouping, arrow-key movement and the "N of M"
        // announcement come from the platform rather than from ARIA attributes
        // and a hand-rolled roving tabindex.
        <fieldset disabled={answered} className="space-y-2">
          <legend className="sr-only">{question.prompt}</legend>
          {item.optionOrder.map((orig, i) => {
            const chosen = response?.kind === "mc" && response.choice === orig;
            const state: OptionState = !answered
              ? chosen
                ? "selected"
                : "idle"
              : orig === question.answer
                ? "correct"
                : chosen
                  ? "wrong"
                  : "idle";
            return (
              <label key={orig} className={cn(OPTION_BASE, OPTION_CLASS[state])}>
                <input
                  type="radio"
                  name={`q-${question.id}`}
                  className="sr-only"
                  checked={chosen}
                  onChange={() => onResponse({ kind: "mc", choice: orig })}
                />
                <span
                  aria-hidden="true"
                  className="grid size-6 shrink-0 place-items-center rounded-full border border-foreground/25 text-xs font-bold"
                >
                  {LETTERS[i]}
                </span>
                <span className="flex-1">{question.options[orig]}</span>
                <OptionMark state={state} chosen={chosen} />
              </label>
            );
          })}
        </fieldset>
      );
    }

    if (question.kind === "truefalse") {
      return (
        <fieldset disabled={answered} className="space-y-2">
          <legend className="sr-only">{question.prompt}</legend>
          {(["True", "False"] as const).map((label, i) => {
            const value = i === 0;
            const chosen = response?.kind === "truefalse" && response.choice === value;
            const state: OptionState = !answered
              ? chosen
                ? "selected"
                : "idle"
              : question.answer === value
                ? "correct"
                : chosen
                  ? "wrong"
                  : "idle";
            return (
              <label key={label} className={cn(OPTION_BASE, "font-medium", OPTION_CLASS[state])}>
                <input
                  type="radio"
                  name={`q-${question.id}`}
                  className="sr-only"
                  checked={chosen}
                  onChange={() => onResponse({ kind: "truefalse", choice: value })}
                />
                <span className="flex-1">{label}</span>
                <OptionMark state={state} chosen={chosen} />
              </label>
            );
          })}
        </fieldset>
      );
    }

    if (question.kind === "multi") {
      const chosenList = response?.kind === "multi" ? response.choices : [];
      return (
        <fieldset disabled={answered} className="space-y-2">
          <legend className="sr-only">{question.prompt} Select all that apply.</legend>
          {item.optionOrder.map((orig, i) => {
            const chosen = chosenList.includes(orig);
            const state: OptionState = !answered
              ? chosen
                ? "selected"
                : "idle"
              : question.answers.includes(orig)
                ? "correct"
                : chosen
                  ? "wrong"
                  : "idle";
            return (
              <label key={orig} className={cn(OPTION_BASE, OPTION_CLASS[state])}>
                <input
                  type="checkbox"
                  className="sr-only"
                  checked={chosen}
                  onChange={() =>
                    onResponse({
                      kind: "multi",
                      choices: chosen
                        ? chosenList.filter((c) => c !== orig)
                        : [...chosenList, orig],
                    })
                  }
                />
                <span
                  aria-hidden="true"
                  className={cn(
                    "grid size-6 shrink-0 place-items-center rounded border text-xs font-bold",
                    chosen
                      ? "border-primary bg-primary text-primary-foreground"
                      : "border-control-border",
                  )}
                >
                  {chosen ? "✓" : LETTERS[i]}
                </span>
                <span className="flex-1">{question.options[orig]}</span>
                <OptionMark state={state} chosen={chosen} />
              </label>
            );
          })}
        </fieldset>
      );
    }

    // numeric — labelled by the prompt itself rather than by a second copy of it
    const unit = question.unit ? unitLabel(question.unit) : null;
    const value =
      response?.kind === "numeric" && Number.isFinite(response.value) ? String(response.value) : "";
    return (
      <div className="flex items-center gap-2">
        <Input
          type="number"
          inputMode="decimal"
          aria-labelledby={promptId}
          aria-describedby={unit ? `${promptId}-unit` : undefined}
          disabled={answered}
          value={value}
          onChange={(e) =>
            onResponse({
              kind: "numeric",
              value: e.target.value === "" ? NaN : Number(e.target.value),
            })
          }
          className={cn(
            "min-h-11 max-w-40",
            answered && (correct ? "border-success" : "border-destructive"),
          )}
        />
        {unit && (
          <span id={`${promptId}-unit`} className="text-sm text-muted-foreground">
            {unit}
          </span>
        )}
      </div>
    );
  }
}
