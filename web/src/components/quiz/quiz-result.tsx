"use client";

import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import type { ScoreResult } from "@/lib/quiz/session";
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

export function QuizResult({
  result,
  bestPct,
  isNewBest,
  onRetry,
  onRetryMissed,
  onBack,
}: {
  result: ScoreResult;
  bestPct: number;
  isNewBest: boolean;
  onRetry: () => void;
  onRetryMissed: () => void;
  onBack: () => void;
}) {
  const pct = result.total === 0 ? 0 : Math.round((result.correct / result.total) * 100);

  return (
    <Card className="p-6 text-center">
      <div className="flex flex-col items-center">
        <ProgressRing pct={pct} size={128} stroke={10}>
          <span className="flex flex-col leading-tight">
            <span className="text-2xl font-bold">
              {result.correct}/{result.total}
            </span>
            <span className="text-xs text-muted-foreground">{pct}% correct</span>
          </span>
        </ProgressRing>
        <h2 className="mt-4 text-lg font-bold">
          {isNewBest ? "Nicely done — new best for this chapter!" : "Practice complete"}
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">Your best so far: {bestPct}%</p>
      </div>

      {result.missed.length > 0 && (
        <div className="mt-6 text-left">
          <p className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Review your {result.missed.length}{" "}
            {result.missed.length === 1 ? "miss" : "misses"}
          </p>
          <div className="space-y-2">
            {result.missed.map((q) => (
              <div key={q.id} className="rounded-lg border p-3">
                <div className="text-sm font-medium">{q.prompt}</div>
                <div className="mt-1 text-xs text-emerald-600 dark:text-emerald-400">
                  Correct: {correctText(q)}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="mt-6 flex flex-wrap justify-center gap-2">
        <Button onClick={onRetry}>Retry chapter</Button>
        <Button variant="outline" onClick={onRetryMissed} disabled={result.missed.length === 0}>
          Practice {result.missed.length} missed
        </Button>
        <Button variant="ghost" onClick={onBack}>
          Back to chapters
        </Button>
      </div>
    </Card>
  );
}
