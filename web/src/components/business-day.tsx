"use client";

import { CalendarClock, ChevronRight } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { formatBusinessDate } from "@/lib/dates";
import { describeMovements } from "@/lib/movements";
import { useAdvanceDay, useClock } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { DayReport } from "@/lib/types";

// The deployment's business date, and the button that advances it.
//
// # Why it is on every shell
//
// Every date on every screen is the deployment's, and the deployment starts
// about a year behind the wall clock. A reader who cannot see which day it is on
// cannot tell a stale seeded date from today's, and then reads every screen
// wrong. So this is not decoration: it is the frame that makes the rest of the
// dates mean something.
//
// It is in the topbar rather than the console sidebar because the sidebar does
// not render for a customer, and a customer's screen shows seeded dates too.
// A customer advancing the world is a fiction no retail bank has; it survives
// because the identity picker two inches away already lets you become the
// settlement agent, so that topbar is the operator's frame around a persona
// rather than the persona's own chrome.
//
// # Why it dials the settlement agent from wherever it is standing
//
// Both routes are on that listener, under the argument that advancing the day
// drives all N+2 institutions and is therefore one operator's act over a
// DEPLOYMENT. This is exactly what the reset button does, and the same ruling
// covers it: correct rather than awkward wherever you happen to be standing.
//
// # Why there is no confirm
//
// A day destroys nothing, and it is the app's central teaching interaction — a
// modal would be paid for on every click. What acknowledges it is the date
// changing in place; what the toast carries is the report. The day is one-way,
// which puts it nearer the reset button than to anything else on the screen:
// only a reset rewinds it.
export function BusinessDay() {
  const clock = useClock();
  const advance = useAdvanceDay();

  const date = clock.data;
  // A closed day says WHY when the closure has a name, and only that it is
  // closed when it does not: a weekend is shut for no reason the calendar names.
  const closure = date && !date.settlementDay ? (date.closure ?? "weekend") : undefined;

  return (
    <div className="flex items-center gap-1">
      <span
        className="hidden items-center gap-1.5 text-sm text-muted-foreground sm:flex"
        title={closure ? `TARGET closed — ${closure}` : "TARGET open"}
      >
        <CalendarClock className="size-4" />
        <span className="tabular-nums">{formatBusinessDate(date?.date)}</span>
        {closure && (
          <span className="rounded bg-muted px-1.5 py-0.5 text-xs">closed · {closure}</span>
        )}
      </span>
      <Button
        variant="outline"
        size="sm"
        className="gap-1"
        // Disabled while a day is running, because there is ONE deployment
        // behind every shell and a second advance would queue behind the first
        // on the same lock the reset takes.
        disabled={advance.isPending}
        onClick={async () => {
          try {
            const report = await advance.mutateAsync();
            toast.success(
              `${formatBusinessDate(report.ran.date)} → ${formatBusinessDate(report.next.date)}`,
              { description: describeDay(report) },
            );
          } catch (e) {
            toast.error(describeError(e));
          }
        }}
      >
        <ChevronRight className="size-4" />
        <span className="hidden sm:inline">Advance day</span>
        <span className="sm:hidden">Day</span>
      </Button>
    </div>
  );
}

// describeDay is the one line the toast carries: what the day did, in the terms
// the report is made of. A phase's door says the same thing the same way; see
// describeMovements.
//
// A day the scheme was shut says so instead of counting zeros. That is the
// lesson rather than an empty result — interest still accrued, and nothing
// cleared because nothing may.
function describeDay(report: DayReport): string {
  if (!report.ran.settlementDay) {
    const why = report.ran.closure ?? "weekend";
    return `TARGET closed (${why}) — interest accrued, nothing cleared`;
  }
  return describeMovements(report);
}
