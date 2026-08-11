"use client";

import Link from "next/link";
import { useParams } from "next/navigation";

import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { QuizRunner } from "@/components/quiz/quiz-runner";
import { getChapter } from "@/lib/quiz";

export default function ChapterQuizPage() {
  const { chapter: slug } = useParams<{ chapter: string }>();
  const chapter = getChapter(slug);

  if (!chapter) {
    return (
      <div className="space-y-4">
        <PageHeader title="Chapter not found" />
        <Button asChild variant="outline">
          <Link href="/learn">← Back to Learn</Link>
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* The only way out of a session used to be the topbar, which leads to the
          persona lobby rather than back to the chapter list. */}
      <Link
        href="/learn"
        className="inline-flex min-h-11 items-center text-sm text-muted-foreground hover:text-foreground sm:min-h-0"
      >
        ← All chapters
      </Link>
      <PageHeader
        title={`Chapter ${chapter.number} · ${chapter.title}`}
        description="Answer, check, and learn from the explanation. Your place and your best score are saved on this device."
      />
      <QuizRunner slug={chapter.slug} title={chapter.title} questions={chapter.questions} />
    </div>
  );
}
