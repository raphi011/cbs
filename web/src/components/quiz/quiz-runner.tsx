"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";

import { useConceptPanel } from "@/components/concept-panel-provider";
import {
  buildSession,
  isCorrect,
  score,
  type Response,
  type SessionItem,
} from "@/lib/quiz/session";
import type { Question } from "@/lib/quiz/types";
import { readProgress, recordResult } from "@/lib/quiz/storage";
import { ProgressRing } from "./progress-ring";
import { QuestionCard } from "./question-card";
import { QuizResult } from "./quiz-result";

export function QuizRunner({
  slug,
  questions,
}: {
  slug: string;
  title: string;
  questions: Question[];
}) {
  const router = useRouter();
  const { setDefaultConcept, togglePanel } = useConceptPanel();

  const [seed, setSeed] = useState(() => Date.now());
  const [pool, setPool] = useState<Question[]>(questions);
  const session = useMemo<SessionItem[]>(() => buildSession(pool, seed), [pool, seed]);

  const [index, setIndex] = useState(0);
  const [responses, setResponses] = useState<(Response | null)[]>(() => session.map(() => null));
  const [phase, setPhase] = useState<"answering" | "answered">("answering");
  const [finished, setFinished] = useState(false);
  const [streak, setStreak] = useState(0);
  const [recorded, setRecorded] = useState<{ bestPct: number; isNewBest: boolean } | null>(null);
  // Whether the reader has chosen to reveal the current question's concept early.
  const [conceptRevealed, setConceptRevealed] = useState(false);

  // A new session (retry / retry-missed) resets all per-session state.
  useEffect(() => {
    /* eslint-disable react-hooks/set-state-in-effect */
    setResponses(session.map(() => null));
    setIndex(0);
    setPhase("answering");
    setFinished(false);
    setStreak(0);
    setRecorded(null);
    setConceptRevealed(false);
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [session]);

  const current = session[index];

  // Drive the right sidebar with the current concept — but withhold it while the
  // reader is still answering (it often gives the answer away). It appears once
  // the answer is submitted, or earlier if the reader taps "Show the concept".
  useEffect(() => {
    const show = phase === "answered" || conceptRevealed;
    setDefaultConcept(show ? (current?.question.concept ?? null) : null);
    return () => setDefaultConcept(null);
  }, [current, phase, conceptRevealed, setDefaultConcept]);

  if (session.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No questions in this chapter yet — check back soon.
      </p>
    );
  }

  const result = score(session, responses);

  if (finished) {
    return (
      <QuizResult
        result={result}
        bestPct={recorded?.bestPct ?? 0}
        isNewBest={recorded?.isNewBest ?? false}
        onRetry={() => {
          setPool(questions);
          setSeed(Date.now());
        }}
        onRetryMissed={() => {
          setPool(result.missed);
          setSeed(Date.now());
        }}
        onBack={() => router.push("/learn")}
      />
    );
  }

  if (!current) return null; // transient: session changed, reset effect pending

  const answeredCount = responses.filter((r) => r !== null).length;
  const correctSoFar = session.reduce(
    (n, item, i) => n + (isCorrect(item.question, responses[i] ?? null) ? 1 : 0),
    0,
  );
  const progressPct = Math.round(((index + 1) / session.length) * 100);

  function setResponse(r: Response) {
    setResponses((prev) => {
      const next = [...prev];
      next[index] = r;
      return next;
    });
  }

  function check() {
    setPhase("answered");
    setStreak((s) => (isCorrect(current.question, responses[index] ?? null) ? s + 1 : 0));
  }

  function next() {
    if (index + 1 >= session.length) {
      const finalPct = Math.round((score(session, responses).correct / session.length) * 100);
      const prevBest = readProgress(slug)?.bestPct ?? 0;
      const saved = recordResult(slug, finalPct, new Date().toISOString());
      setRecorded({ bestPct: saved.bestPct, isNewBest: finalPct > prevBest });
      setFinished(true);
    } else {
      setIndex((i) => i + 1);
      setPhase("answering");
      setConceptRevealed(false);
    }
  }

  // Reveal the current concept early (a deliberate hint) and open the panel.
  function revealConcept() {
    setConceptRevealed(true);
    togglePanel();
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <ProgressRing pct={progressPct} size={44}>
          {index + 1}/{session.length}
        </ProgressRing>
        <span className="text-sm text-muted-foreground">
          Score {correctSoFar}/{answeredCount}
        </span>
        {streak >= 2 && (
          <span className="ml-auto rounded-full bg-amber-50 px-2.5 py-0.5 text-xs font-bold text-amber-700 dark:bg-amber-950/40 dark:text-amber-300">
            🔥 {streak} in a row
          </span>
        )}
      </div>

      <QuestionCard
        item={current}
        response={responses[index] ?? null}
        phase={phase}
        conceptRevealed={conceptRevealed}
        onResponse={setResponse}
        onRevealConcept={revealConcept}
        onCheck={check}
        onNext={next}
        isLast={index + 1 >= session.length}
      />
    </div>
  );
}
