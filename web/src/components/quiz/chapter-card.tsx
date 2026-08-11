import Link from "next/link";

import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import type { Chapter } from "@/lib/quiz/types";
import type { ChapterProgress } from "@/lib/quiz/storage";

export function ChapterCard({
  chapter,
  progress,
}: {
  chapter: Chapter;
  progress: ChapterProgress | null;
}) {
  const count = chapter.questions.length;
  const empty = count === 0;

  const pill = empty
    ? "Coming soon"
    : progress == null
      ? "Not started"
      : progress.bestPct >= 100
        ? "100%"
        : `Best ${progress.bestPct}%`;

  // Only a finished chapter is filled. Everything else is an outline, because
  // muted text on `--muted` measures 4.34:1 and the same text on the card is
  // 4.74:1 — and because a grid of eighteen filled pills has no hierarchy to
  // read off it.
  const pillClass =
    !empty && progress != null && progress.bestPct >= 100
      ? "bg-success-muted text-success-strong"
      : "border text-muted-foreground";

  return (
    <Card
      size="sm"
      className={cn(
        // `relative` anchors the stretched link below; `overflow-hidden` is what
        // keeps a full-bleed child inside the card's rounded corners.
        "relative h-full overflow-hidden p-4 transition-shadow",
        empty ? "opacity-60" : "hover:shadow-md",
      )}
    >
      <div className="text-xs font-bold text-muted-foreground">CHAPTER {chapter.number}</div>

      {/* A heading, so eighteen chapter titles are eighteen navigable landmarks
          rather than eighteen anonymous divs. The link wraps the title alone and
          stretches over the card, which is what keeps the accessible name
          "What a Bank Is" instead of the card's entire text content. */}
      <h3 className="mt-1 text-sm font-semibold leading-snug">
        {empty ? (
          chapter.title
        ) : (
          <Link
            href={`/learn/${chapter.slug}`}
            className="after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-3 focus-visible:after:ring-ring/50"
          >
            {chapter.title}
          </Link>
        )}
      </h3>

      <div className="mt-3 flex items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">
          {empty ? "—" : `${count} ${count === 1 ? "question" : "questions"}`}
        </span>
        <span className={cn("rounded-full px-2 py-0.5 text-xs font-bold", pillClass)}>{pill}</span>
      </div>
    </Card>
  );
}
