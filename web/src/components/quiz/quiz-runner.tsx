"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";

import { useConceptPanel } from "@/components/concept-panel-provider";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  buildSession,
  isCorrect,
  score,
  type Response,
  type SessionItem,
} from "@/lib/quiz/session";
import type { Question } from "@/lib/quiz/types";
import {
  clearSession,
  readMissedConcepts,
  readProgress,
  readSession,
  recordConceptOutcomes,
  recordResult,
  writeSession,
} from "@/lib/quiz/storage";
import { ProgressRing } from "./progress-ring";
import { QuestionCard } from "./question-card";
import { QuizResult } from "./quiz-result";

/** What a run is drawn from, and how a resumed run is recognised. */
interface Run {
  seed: number;
  pool: Question[];
  /** Absent for a fresh run; the ids a saved run expects when resuming. */
  resumeIds?: string[];
}

export function QuizRunner({
  slug,
  questions,
  limit,
}: {
  slug: string;
  title: string;
  questions: Question[];
  /** Cap the session. Omit to ask every question in `questions`. */
  limit?: number;
}) {
  const router = useRouter();
  const { setDefaultConcept, togglePanel } = useConceptPanel();

  // A fresh shuffle every visit means the seed is random, and a random seed
  // cannot be rendered on the server: the server would pick one order and the
  // client another, which is verbatim the hydration mismatch Next names
  // ("Variable input such as `Date.now()` or `Math.random()`"). React recovers
  // by throwing the server tree away, so the only visible trace was a
  // <TypeBadge> whose kind disagreed — the symptom, one level down from the
  // cause. So the run starts null and is set on mount, the same shape as the
  // localStorage read on /learn, and until it lands there is no session to
  // render rather than a differently-shuffled one.
  const [run, setRun] = useState<Run | null>(null);
  // Read once on mount, not per render: recording an outcome mid-session would
  // otherwise reshuffle the session the reader is part-way through.
  const [favour, setFavour] = useState<ReadonlySet<string> | null>(null);

  const session = useMemo<SessionItem[]>(
    () => (run === null ? [] : buildSession(run.pool, run.seed, { limit, favour: favour ?? undefined })),
    [run, limit, favour],
  );

  const [index, setIndex] = useState(0);
  const [responses, setResponses] = useState<(Response | null)[]>([]);
  const [phase, setPhase] = useState<"answering" | "answered">("answering");
  const [finished, setFinished] = useState(false);
  const [streak, setStreak] = useState(0);
  const [bestStreak, setBestStreak] = useState(0);
  const [recorded, setRecorded] = useState<{ bestPct: number; isNewBest: boolean } | null>(null);
  // Whether the reader has chosen to reveal the current question's concept early.
  const [conceptRevealed, setConceptRevealed] = useState(false);
  const [resumed, setResumed] = useState(false);

  useEffect(() => {
    // Guarded so React's double-invoked mount effect in dev does not reshuffle
    // a session the reader has already seen.
    /* eslint-disable react-hooks/set-state-in-effect */
    setFavour((f) => f ?? readMissedConcepts());
    setRun((r) => {
      if (r) return r;
      const saved = readSession(slug);
      return saved
        ? { seed: saved.seed, pool: questions, resumeIds: saved.ids }
        : { seed: Date.now(), pool: questions };
    });
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [slug, questions]);

  // Per-session state follows the session. A resume adopts the saved answers,
  // but only once the rebuilt session is confirmed to be the same questions in
  // the same order — see `SavedSession.ids`.
  useEffect(() => {
    if (session.length === 0) return;
    /* eslint-disable react-hooks/set-state-in-effect */
    const saved = run?.resumeIds ? readSession(slug) : null;
    const matches =
      saved != null &&
      saved.ids.length === session.length &&
      saved.ids.every((id, i) => id === session[i].question.id);

    if (saved && matches) {
      setResponses(saved.responses.slice(0, session.length));
      setIndex(Math.min(saved.index, session.length - 1));
      setStreak(saved.streak);
      setBestStreak(saved.streak);
      setResumed(true);
    } else {
      if (saved && !matches) clearSession(slug);
      setResponses(session.map(() => null));
      setIndex(0);
      setStreak(0);
      setBestStreak(0);
      setResumed(false);
    }
    setPhase("answering");
    setFinished(false);
    setRecorded(null);
    setConceptRevealed(false);
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [session, run, slug]);

  const current = session[index];

  // Questions before `index` were necessarily checked to get past them, so the
  // count is positional rather than a tally of non-null responses — which
  // counted a *selection* and told a reader their score had moved before they
  // had submitted anything.
  const checkedCount = index + (phase === "answered" ? 1 : 0);
  const correctSoFar = session
    .slice(0, checkedCount)
    .reduce((n, item, i) => n + (isCorrect(item.question, responses[i] ?? null) ? 1 : 0), 0);

  // Drive the right sidebar with the current concept — but withhold it while the
  // reader is still answering (it often gives the answer away). It appears once
  // the answer is submitted, or earlier if the reader taps "Show the concept".
  // On the result screen there is no current question, so it clears.
  useEffect(() => {
    const show = !finished && (phase === "answered" || conceptRevealed);
    setDefaultConcept(show ? (current?.question.concept ?? null) : null);
    return () => setDefaultConcept(null);
  }, [current, phase, conceptRevealed, finished, setDefaultConcept]);

  // Persist after every state change that a resume has to reproduce. Writing
  // here rather than in the handlers means an answer is saved the moment it is
  // recorded, so nothing is lost by closing the tab mid-question.
  useEffect(() => {
    if (session.length === 0 || finished) return;
    writeSession(slug, {
      seed: run?.seed ?? 0,
      index,
      ids: session.map((i) => i.question.id),
      responses,
      streak,
    });
  }, [slug, run, session, index, responses, streak, finished]);

  const finish = useCallback(
    (upTo: number) => {
      const answered = session.slice(0, upTo);
      const result = score(answered, responses.slice(0, upTo));
      const finalPct =
        answered.length === 0 ? 0 : Math.round((result.correct / answered.length) * 100);
      const prevBest = readProgress(slug)?.bestPct ?? 0;
      const saved = recordResult(slug, finalPct, new Date().toISOString());

      const missedIds = new Set(result.missed.map((m) => m.question.id));
      recordConceptOutcomes(
        answered.filter((i) => missedIds.has(i.question.id) && i.question.concept)
          .map((i) => i.question.concept as string),
        answered.filter((i) => !missedIds.has(i.question.id) && i.question.concept)
          .map((i) => i.question.concept as string),
      );

      clearSession(slug);
      setRecorded({ bestPct: saved.bestPct, isNewBest: finalPct > prevBest });
      setFinished(true);
    },
    [session, responses, slug],
  );

  // Pre-mount: the shuffle has no seed yet, so nothing can be shown. Distinct
  // from an empty chapter below, which is a fact about the content.
  if (run === null) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-11 w-48" />
        <Skeleton className="h-64" />
      </div>
    );
  }

  if (session.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No questions in this chapter yet — check back soon.
      </p>
    );
  }

  if (finished) {
    // Scored over what was actually asked, so ending early reports the answers
    // given rather than counting the unasked remainder as wrong.
    const asked = Math.max(checkedCount, 1);
    return (
      <QuizResult
        result={score(session.slice(0, asked), responses.slice(0, asked))}
        bestPct={recorded?.bestPct ?? 0}
        isNewBest={recorded?.isNewBest ?? false}
        bestStreak={bestStreak}
        endedEarly={asked < session.length}
        totalInSession={session.length}
        onRetry={() => startRun(questions)}
        onRetryMissed={(missed) => startRun(missed)}
        onBack={() => router.push("/learn")}
      />
    );
  }

  if (!current) return null; // transient: session changed, reset effect pending

  const progressPct = Math.round(((index + 1) / session.length) * 100);

  function startRun(pool: Question[]) {
    clearSession(slug);
    setRun({ seed: Date.now(), pool });
  }

  function setResponse(r: Response) {
    setResponses((prev) => {
      const next = [...prev];
      next[index] = r;
      return next;
    });
  }

  function check() {
    setPhase("answered");
    const nextStreak = isCorrect(current.question, responses[index] ?? null) ? streak + 1 : 0;
    setStreak(nextStreak);
    setBestStreak((b) => Math.max(b, nextStreak));
  }

  function next() {
    if (index + 1 >= session.length) {
      finish(session.length);
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
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <ProgressRing pct={progressPct} size={44} label={`Question ${index + 1} of ${session.length}`}>
          {index + 1}/{session.length}
        </ProgressRing>
        <span className="text-sm text-muted-foreground">
          {checkedCount === 0 ? (
            "No answers checked yet"
          ) : (
            <>
              Score {correctSoFar}/{checkedCount}
            </>
          )}
        </span>
        {streak >= 2 && (
          <span className="rounded-full bg-success-muted px-2.5 py-0.5 text-xs font-bold text-success-strong">
            {streak} in a row
          </span>
        )}
        <Button
          variant="ghost"
          size="sm"
          onClick={() => finish(checkedCount)}
          disabled={checkedCount === 0}
          className="ml-auto min-h-11 text-muted-foreground sm:min-h-9"
        >
          End session
        </Button>
      </div>

      {resumed && index > 0 && (
        <p className="text-sm text-muted-foreground" role="status">
          Picking up where you left off — question {index + 1} of {session.length}.
        </p>
      )}

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
