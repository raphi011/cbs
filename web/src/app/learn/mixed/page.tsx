"use client";

import Link from "next/link";

import { PageHeader } from "@/components/page-header";
import { QuizRunner } from "@/components/quiz/quiz-runner";
import { mixedQuestions, SESSION_SIZE } from "@/lib/quiz";

export default function MixedQuizPage() {
  const questions = mixedQuestions();

  return (
    <div className="space-y-6">
      <Link
        href="/learn"
        className="inline-flex min-h-11 items-center text-sm text-muted-foreground hover:text-foreground sm:min-h-0"
      >
        ← All chapters
      </Link>
      <PageHeader
        title="Mixed review"
        description={`${SESSION_SIZE} questions drawn from every chapter, weighted towards the concepts you have missed before. Leaving keeps your place.`}
      />
      {/* The pool is every question in the book; the session is SESSION_SIZE of
          them. Passing the pool unlimited is what made this a 380-question sitting. */}
      <QuizRunner
        slug="mixed"
        title="Mixed review"
        questions={questions}
        limit={SESSION_SIZE}
      />
    </div>
  );
}
