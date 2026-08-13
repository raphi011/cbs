"use client";

import { useState } from "react";
import { Play } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { describeError } from "@/lib/api/errors";
import { usePhases, useRunPhase } from "@/lib/api/hooks";
import { describeMovements } from "@/lib/movements";
import type { Phase } from "@/lib/types";

// A door per phase of the business day, so a reader can run the clearing and
// stop. Advancing the whole day is the topbar's; this is the same act at a
// finer grain, and it is what makes the mesh beside it worth watching — without
// it the picture only ever changes when somebody runs eleven phases at once.
//
// The doors sit here rather than beside the day's button because stepping is
// only interesting next to the thing it moves.
//
// # What it costs
//
// Stepping a phase by hand and then advancing the day runs that phase twice,
// and nothing refuses it: nothing records how far a day has got. The design
// record for the message log says so, and names the record that fixes it.

export function PhaseStepper() {
  const { data: phases, isLoading } = usePhases();
  const run = useRunPhase();
  // Which door is open. The mutation's own `isPending` cannot say, because
  // there is one mutation behind every button.
  const [running, setRunning] = useState<string | null>(null);

  if (isLoading) return <Skeleton className="h-24" />;
  if (!phases || phases.length === 0) return null;

  return (
    <ol className="divide-y rounded-lg border">
      {phases.map((phase) => (
        <li key={phase.key} className="flex items-center gap-2 py-1.5 pl-3 pr-1.5">
          <span className="flex min-w-0 flex-1 items-center gap-1.5">
            <span className="truncate text-sm">{phase.name}</span>
            <PhaseMarks phase={phase} />
          </span>
          <Button
            variant="ghost"
            size="icon"
            aria-label={`Run ${phase.name}`}
            title={`Run ${phase.name}`}
            // Every door is disabled while any one is open: there is ONE
            // deployment behind every shell, and a second run would queue
            // behind the first on the same lock the day takes.
            disabled={running !== null}
            onClick={async () => {
              setRunning(phase.key);
              try {
                const report = await run.mutateAsync(phase.key);
                toast.success(report.phase.name, {
                  description: describeMovements(report),
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
