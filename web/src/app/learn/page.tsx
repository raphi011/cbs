"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { Shuffle } from "lucide-react";

import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ChapterCard } from "@/components/quiz/chapter-card";
import { chapters, chaptersByPart, SESSION_SIZE } from "@/lib/quiz";
import { readProgress, type ChapterProgress } from "@/lib/quiz/storage";

export default function LearnPage() {
  const parts = useMemo(() => chaptersByPart(), []);
  const [progress, setProgress] = useState<Record<string, ChapterProgress | null>>({});

  useEffect(() => {
    const next: Record<string, ChapterProgress | null> = {};
    for (const part of parts) {
      for (const c of part.chapters) next[c.slug] = readProgress(c.slug);
    }
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setProgress(next);
  }, [parts]);

  // One recommended next step, so the index answers "where do I start" instead
  // of presenting nineteen equal doors. Unstarted chapters come first in book
  // order; once every chapter has been attempted, the first imperfect one is
  // what is worth another pass.
  const next = useMemo(() => {
    const unstarted = chapters.find((c) => c.questions.length > 0 && !progress[c.slug]);
    if (unstarted) return { chapter: unstarted, resuming: false };
    const imperfect = chapters.find(
      (c) => c.questions.length > 0 && (progress[c.slug]?.bestPct ?? 0) < 100,
    );
    return imperfect ? { chapter: imperfect, resuming: true } : null;
  }, [progress]);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Learn"
        description="Practice the concepts from each chapter. About twenty questions a session, answered one at a time, with the reasoning after each one."
      />

      <Card className="p-5">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
          <div className="min-w-0 flex-1">
            <p className="text-xs font-bold uppercase tracking-wide text-muted-foreground">
              {next ? (next.resuming ? "Worth another pass" : "Start here") : "Every chapter cleared"}
            </p>
            <p className="mt-1 text-base font-semibold">
              {next
                ? `Chapter ${next.chapter.number} · ${next.chapter.title}`
                : "Nothing left unfinished"}
            </p>
            <p className="mt-1 text-sm text-muted-foreground">
              {next
                ? next.resuming
                  ? `Your best here is ${progress[next.chapter.slug]?.bestPct ?? 0}%.`
                  : "The chapters build on each other, so this is the one to take first."
                : "Mixed review keeps drawing from all eighteen."}
            </p>
          </div>
          <div className="flex shrink-0 flex-wrap gap-2">
            {next && (
              <Button asChild className="min-h-11">
                <Link href={`/learn/${next.chapter.slug}`}>
                  {next.resuming ? "Practise again" : "Start chapter"}
                </Link>
              </Button>
            )}
            <Button asChild variant={next ? "outline" : "default"} className="min-h-11 gap-2">
              <Link href="/learn/mixed">
                <Shuffle aria-hidden="true" className="size-4" />
                Mixed review · {SESSION_SIZE} questions
              </Link>
            </Button>
          </div>
        </div>
      </Card>

      {parts.map((part) => (
        <section key={part.name} className="space-y-3">
          <h2 className="text-xs font-bold uppercase tracking-wide text-muted-foreground">
            {part.name}
          </h2>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {part.chapters.map((c) => (
              <ChapterCard key={c.slug} chapter={c} progress={progress[c.slug] ?? null} />
            ))}
          </div>
        </section>
      ))}

      {/* The three words a question is tagged with, said once where every
          chapter is in view, rather than as a bare label beside each question. */}
      <p className="text-xs text-muted-foreground">
        Questions are tagged <strong className="font-semibold text-foreground">Intro</strong> for a
        chapter&rsquo;s starting idea, <strong className="font-semibold text-foreground">Core</strong>{" "}
        for its main argument and{" "}
        <strong className="font-semibold text-foreground">Challenge</strong> for the edge cases. A
        session asks them in that order.
      </p>
    </div>
  );
}
