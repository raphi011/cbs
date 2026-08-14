"use client";

import { useState } from "react";
import { FlaskConical } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { formatBusinessDate } from "@/lib/dates";
import { describeMovements } from "@/lib/movements";
import { useRunScenario, useScenarios } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { Scenario, ScenarioReport } from "@/lib/types";

// The scenarios an operator can trigger, beside the day controls.
//
// # Why the app needs one at all
//
// A deployment boots holding a BASE STATE: four banks, admitted to the scheme,
// subscribed to the routing directory, priced and prefunded, one depositor
// each, and nothing else. There is no payment and no facility until somebody
// runs one of these or sends one by hand, so this menu is where most readers
// arriving at an empty screen are meant to come first.
//
// # Why it warns
//
// Three costs, and each is stated before the click rather than discovered:
// a scenario advances the SHARED business date, often by months, so every other
// tab moves with it; two cannot run side by side, because there is one clock and
// one set of databases behind every shell; and there is no undo — resetting the
// data is the only way back, exactly as it is for the day.
//
// # Why it dials the settlement agent from wherever it is standing
//
// The same ruling that covers the day's button and the reset: a scenario drives
// all N+2 institutions, so it is one operator's act over a DEPLOYMENT rather
// than an act of whichever institution's screen you happen to be on.
export function ScenarioPicker() {
  const { data: scenarios } = useScenarios();
  const run = useRunScenario();
  // Which one is running. The mutation's own `isPending` cannot say, because
  // there is one mutation behind every item.
  const [running, setRunning] = useState<string | null>(null);

  if (!scenarios || scenarios.length === 0) return null;

  async function trigger(scenario: Scenario) {
    setRunning(scenario.id);
    const pending = toast.loading(scenario.name, {
      description: "Running — this advances the business date.",
    });
    try {
      const report = await run.mutateAsync(scenario.id);
      toast.success(scenario.name, { id: pending, description: describeRun(report) });
    } catch (e) {
      toast.error(describeError(e), { id: pending });
    } finally {
      setRunning(null);
    }
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="gap-1"
          // Every item is disabled while any one is running, because there is ONE
          // deployment behind every shell and a second would queue behind the
          // first on the same lock the day takes.
          disabled={running !== null}
        >
          <FlaskConical className="size-4" />
          <span className="hidden sm:inline">Scenarios</span>
        </Button>
      </DropdownMenuTrigger>
      {/* An explicit width, because the shared content is sized to its TRIGGER
          and this trigger is one short word: at that width every description
          below wraps to about a word a line. Capped against the viewport so the
          menu still fits on a phone. */}
      <DropdownMenuContent align="end" className="w-[24rem] max-w-[calc(100vw-2rem)]">
        <DropdownMenuLabel className="font-normal text-muted-foreground">
          Each one drives the real doors an operator has, and each moves the shared
          business date. There is no undo — only a reset.
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        {scenarios.map((scenario) => (
          <DropdownMenuItem
            key={scenario.id}
            disabled={running !== null}
            className="flex-col items-start gap-0.5 whitespace-normal"
            onSelect={() => void trigger(scenario)}
          >
            <span className="font-medium">{scenario.name}</span>
            <span className="text-xs text-muted-foreground">{scenario.description}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// describeRun is the one line the toast carries: where the date moved to, and
// what the scenario did on the way. The date comes first because it is the cost
// the menu warned about, and the only one a reader can check afterwards.
function describeRun(report: ScenarioReport): string {
  const moved = `${formatBusinessDate(report.ran.date)} → ${formatBusinessDate(report.next.date)}`;
  return `${moved} · ${describeMovements(report)}`;
}
