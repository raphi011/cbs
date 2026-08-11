"use client";

import { useState } from "react";
import { ChevronDown, Lightbulb } from "lucide-react";

import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ConceptMarkdown } from "@/components/concept-markdown";
import { useConceptPanel } from "@/components/concept-panel-provider";
import { hintContent } from "@/components/hint-content";
import { cn } from "@/lib/utils";
import type { MissedQuestion, Response, ScoreResult } from "@/lib/quiz/session";
import type { Question } from "@/lib/quiz/types";
import { formatNumericAnswer } from "@/lib/quiz/units";
import { ProgressRing } from "./progress-ring";

function correctText(q: Question): string {
  switch (q.kind) {
    case "mc":
      return q.options[q.answer];
    case "truefalse":
      return q.answer ? "True" : "False";
    case "multi":
      return q.answers.map((i) => q.options[i]).join(", ");
    case "numeric":
      return formatNumericAnswer(q.answer, q.unit);
  }
}

/** What the reader actually answered, in the same words the question used. */
function responseText(q: Question, r: Response | null): string {
  if (!r || r.kind !== q.kind) return "No answer";
  switch (r.kind) {
    case "mc":
      return q.kind === "mc" ? (q.options[r.choice] ?? "No answer") : "No answer";
    case "truefalse":
      return r.choice ? "True" : "False";
    case "multi":
      if (q.kind !== "multi" || r.choices.length === 0) return "No answer";
      return r.choices.map((i) => q.options[i]).join(", ");
    case "numeric":
      return Number.isFinite(r.value)
        ? formatNumericAnswer(r.value, q.kind === "numeric" ? q.unit : undefined)
        : "No answer";
  }
}

/**
 * The headline a reader leaves with.
 *
 * A new best is only worth congratulating if the score is one worth repeating —
 * every first attempt beats a stored zero, so gating on "new best" alone
 * congratulated a quarter of the answers being right. Praise that arrives for
 * any result is praise that means nothing when it arrives for a good one.
 */
function verdict(pct: number, isNewBest: boolean, endedEarly: boolean): string {
  if (endedEarly) return "Session ended — here is what you answered";
  if (pct === 100) return "Every one right.";
  if (isNewBest && pct >= 70) return "Nicely done — a new best for this chapter.";
  if (pct >= 70) return "Solid round.";
  if (pct >= 40) return "Worth another pass — the misses are below.";
  return "This one needs a second run. Read the explanations first.";
}

export function QuizResult({
  result,
  bestPct,
  isNewBest,
  bestStreak,
  endedEarly,
  totalInSession,
  onRetry,
  onRetryMissed,
  onBack,
}: {
  result: ScoreResult;
  bestPct: number;
  isNewBest: boolean;
  bestStreak: number;
  endedEarly: boolean;
  totalInSession: number;
  onRetry: () => void;
  onRetryMissed: (missed: Question[]) => void;
  onBack: () => void;
}) {
  const pct = result.total === 0 ? 0 : Math.round((result.correct / result.total) * 100);
  const missedQuestions = result.missed.map((m) => m.question);

  return (
    <div className="space-y-4">
      <Card className="p-6 text-center">
        <div className="flex flex-col items-center">
          <ProgressRing pct={pct} size={128} stroke={10} label={`${pct} percent correct`}>
            <span className="flex flex-col leading-tight">
              <span className="text-2xl font-bold">
                {result.correct}/{result.total}
              </span>
              <span className="text-xs text-muted-foreground">correct</span>
            </span>
          </ProgressRing>
          <h2 className="mt-4 text-lg font-bold">{verdict(pct, isNewBest, endedEarly)}</h2>

          {/* One line of context, not three restatements of the same number:
              the ring already says what this round scored. */}
          <p className="mt-1 text-sm text-muted-foreground">
            {endedEarly
              ? `${result.total} of ${totalInSession} answered · best for this chapter ${bestPct}%`
              : `Best for this chapter ${bestPct}%`}
          </p>

          {bestStreak >= 2 && (
            <span className="mt-3 rounded-full bg-success-muted px-3 py-1 text-xs font-bold text-success-strong">
              Longest run: {bestStreak} in a row
            </span>
          )}
        </div>

        {/* Actions sit directly under the score rather than below a long review
            list, where they were most of a phone screen away. */}
        <div className="mt-6 flex flex-wrap justify-center gap-2">
          <Button
            onClick={() => onRetryMissed(missedQuestions)}
            disabled={result.missed.length === 0}
            className="min-h-11"
          >
            Practice {result.missed.length} missed
          </Button>
          <Button variant="outline" onClick={onRetry} className="min-h-11">
            Retry chapter
          </Button>
          <Button variant="ghost" onClick={onBack} className="min-h-11">
            Back to chapters
          </Button>
        </div>
      </Card>

      {result.missed.length > 0 && (
        <section className="space-y-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Review your {result.missed.length} {result.missed.length === 1 ? "miss" : "misses"}
          </h3>
          {result.missed.map((m) => (
            <MissCard key={m.question.id} missed={m} />
          ))}
        </section>
      )}
    </div>
  );
}

/**
 * One miss, with the explanation the reader was shown when they got it wrong.
 *
 * Collapsed by default and open on click: fifteen explanations expanded at once
 * is a wall, and fifteen prompts with no reasons is a list of answers to
 * memorise. The explanation is the thing worth keeping, so it is one tap away
 * rather than gone.
 */
function MissCard({ missed }: { missed: MissedQuestion }) {
  const [open, setOpen] = useState(false);
  const { openConcept } = useConceptPanel();
  const q = missed.question;
  const concept = q.concept && q.concept in hintContent ? q.concept : null;

  return (
    <Card size="sm" className="overflow-hidden p-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-start gap-3 p-4 text-left transition-colors hover:bg-accent"
      >
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">{q.prompt}</div>
          <dl className="mt-2 space-y-1 text-xs">
            <div className="flex gap-2">
              <dt className="shrink-0 text-muted-foreground">You answered</dt>
              <dd className="min-w-0 font-medium text-destructive-strong">
                {responseText(q, missed.response)}
              </dd>
            </div>
            <div className="flex gap-2">
              <dt className="shrink-0 text-muted-foreground">Correct answer</dt>
              <dd className="min-w-0 font-medium text-success-strong">{correctText(q)}</dd>
            </div>
          </dl>
        </div>
        <ChevronDown
          aria-hidden="true"
          className={cn("mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")}
        />
        <span className="sr-only">{open ? "Hide the explanation" : "Show the explanation"}</span>
      </button>

      {open && (
        <div className="border-t px-4 pb-4 pt-3">
          <ConceptMarkdown body={q.explanation} className="text-foreground" />
          {concept && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => openConcept(concept)}
              className="mt-2 min-h-11 gap-1.5 sm:min-h-9"
            >
              <Lightbulb aria-hidden="true" className="size-3.5" />
              Read about {concept.replace(/-/g, " ")}
            </Button>
          )}
        </div>
      )}
    </Card>
  );
}
