"use client";

import { useState } from "react";
import { Check, Play } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { describeError } from "@/lib/api/errors";
import { useClock, usePhases, useRunThrough } from "@/lib/api/hooks";
import { describeMovements } from "@/lib/movements";
import type { Phase, PhaseReport } from "@/lib/types";

// A door per phase of the business day, so a reader can run the clearing and
// stop. Advancing the whole day is the topbar's; this is the same act at a
// finer grain, and it is what makes the mesh beside it worth watching — without
// it the picture only ever changes when somebody runs eleven phases at once.
//
// The doors sit here rather than beside the day's button because stepping is
// only interesting next to the thing it moves.
//
// # What a tick means, and what a door runs
//
// The day records how far it has got, so a phase already run is marked and
// advancing the day runs only the rest. Naming a LATER phase runs the ones still
// outstanding before it and then it, which is the day's own order and not one
// assembled here; a phase the day has already run is run on its own. See
// docs/specs/2026-08-14-a-day-cursor-design.md.

export function PhaseStepper() {
  const { data: phases, isLoading } = usePhases();
  const run = useRunThrough();
  // Which door is open. The mutation's own `isPending` cannot say, because
  // there is one mutation behind every button.
  const [running, setRunning] = useState<string | null>(null);

  if (isLoading) return <Skeleton className="h-24" />;
  if (!phases || phases.length === 0) return null;

  return (
    <div className="space-y-2">
      <DayProgress phases={phases} />
      <ol className="divide-y rounded-lg border">
        {phases.map((phase) => (
          <li key={phase.key} className="flex items-center gap-2 py-1.5 pl-3 pr-1.5">
            <span className="flex min-w-0 flex-1 items-center gap-1.5">
              <span className={cn("truncate text-sm", phase.completed && "text-muted-foreground")}>
                {phase.name}
              </span>
              <PhaseMarks phase={phase} />
            </span>
            {phase.completed && (
              <span
                className="shrink-0"
                title="Run on this day — advancing the day will not run it again"
              >
                <Check role="img" aria-label="run on this day" className="size-3.5 text-success-strong" />
              </span>
            )}
            <Button
              variant="ghost"
              size="icon"
              aria-label={runLabel(phase)}
              title={runLabel(phase)}
              // Every door is disabled while any one is open: there is ONE
              // deployment behind every shell, and a second run would queue
              // behind the first on the same lock the day takes.
              disabled={running !== null}
              onClick={async () => {
                setRunning(phase.key);
                try {
                  const report = await run.mutateAsync(phase.key);
                  toast.success(report.phase.name, {
                    description: describeRun(report),
                  });
                } catch (e) {
                  toast.error(describeError(e));
                } finally {
                  setRunning(null);
                }
              }}
            >
              <Play className={running === phase.key ? "size-4 animate-pulse" : "size-4"} />
            </Button>
          </li>
        ))}
      </ol>
    </div>
  );
}

// What opening this door will do, which is not the same for every row: a phase
// the day has already run is run again on its own, one after the roll runs on
// its own whatever the day has reached, and any other carries the day up to it.
function runLabel(phase: Phase): string {
  if (phase.afterClock) return `Run ${phase.name}`;
  if (phase.completed) return `Run ${phase.name} again`;
  return `Run the day through ${phase.name}`;
}

// What a run did, in one line. The phases are the SERVER's account of what it
// took — the day owns that order, so a count assembled here could disagree with
// what actually ran.
function describeRun(report: PhaseReport): string {
  const moved = describeMovements(report);
  if (report.phases.length <= 1) return moved;
  return `${report.phases.length} phases · ${moved}`;
}

// How far this day has got, in words. The ticks below say which phases; this
// says whether advancing the day is a whole day or the tail of one, which is the
// thing a reader is about to click.
//
// It counts the phases THIS day runs, so a day the scheme is shut on is two
// phases long rather than eleven: the ones a closed day skips were never
// waiting to run.
function DayProgress({ phases }: { phases: Phase[] }) {
  const { data: clock } = useClock();
  const today = phases.filter((p) => clock?.settlementDay !== false || !p.settlementOnly);
  const done = today.filter((p) => p.completed).length;

  if (done === 0) {
    return <p className="text-xs text-muted-foreground">Nothing has run on this day yet.</p>;
  }
  const left = today.length - done;
  return (
    <p className="text-xs text-muted-foreground">
      <span className="font-medium text-foreground tabular-nums">{done}</span>
      {` of ${today.length} run — advancing the day runs `}
      {left === 1 ? "the last one" : `the other ${left}`}.
    </p>
  );
}

// What is true of a phase before it runs. `settlementOnly` is reported and not
// enforced — a caller that named a phase has decided it wants it — and
// `afterClock` says the phase runs once the date has moved, which is where the
// day's own advance sits among the doors.
function PhaseMarks({ phase }: { phase: Phase }) {
  return (
    <>
      {phase.settlementOnly && (
        <Badge variant="ghost" className="shrink-0 px-1 text-muted-foreground" title="Runs only on a day the scheme is open">
          TARGET
        </Badge>
      )}
      {phase.afterClock && (
        <Badge variant="ghost" className="shrink-0 px-1 text-muted-foreground" title="Runs after the date has moved">
          next day
        </Badge>
      )}
    </>
  );
}
