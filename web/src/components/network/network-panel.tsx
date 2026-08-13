"use client";

import { useMemo, useState } from "react";
import { usePathname } from "next/navigation";
import { ChevronDown } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/error-state";
import { cn } from "@/lib/utils";
import { useNetworkFlow } from "@/lib/api/hooks";
import { isResting, restingByWire, wireEnds } from "@/lib/network-graph";
import type { NetworkFlow } from "@/lib/types";
import { CrossingList } from "./crossing-list";
import { FlowGraph } from "./flow-graph";
import { useNetworkWatch } from "./network-watcher";
import { PhaseStepper } from "./phase-stepper";

// The mesh, and the doors that move it. Both arrangements are here because they
// are one object seen at two sizes: the rail is for WATCHING while you work in
// somebody's console, and the lobby's is for READING what crossed.
//
// This is the operator's frame around a persona and never a persona's own
// chrome — the same standing "Advance day" has two inches away in the topbar. A
// bank's own screens must not grow it: a payments table showing another bank's
// traffic is exactly the crossing this placement avoids.

const RAIL_OPEN_KEY = "network-rail-open";

// The rail section, above the concept panel. Desktop only: the rail's mobile
// form is a sheet opened to explain a concept, and a live drawing of the
// network is a different object than the one that sheet is for. On a phone the
// lobby's view is the whole of it.
export function NetworkRail() {
  // Desktop-only (its one caller is DesktopShell), so localStorage is safe in
  // the initializer — it never runs on the server.
  const [open, setOpen] = useState(
    () => typeof localStorage === "undefined" || localStorage.getItem(RAIL_OPEN_KEY) !== "false",
  );
  const onLobby = usePathname() === "/";
  const { data: flow, isLoading, error } = useNetworkFlow();

  const toggle = () => {
    setOpen((prev) => {
      localStorage.setItem(RAIL_OPEN_KEY, String(!prev));
      return !prev;
    });
  };

  // The lobby holds the full-size view, and one screen shows one mesh.
  if (onLobby) return null;

  return (
    <section className="max-h-[55%] flex-none overflow-y-auto border-b">
      <div className="flex items-center gap-1.5 px-4 py-3">
        <h2 className="flex flex-1 items-center gap-1.5 text-sm font-semibold">
          Network
          <Hint id="store-split" />
        </h2>
        <LiveDot />
        <Button
          variant="ghost"
          size="icon"
          aria-label={open ? "Collapse the network" : "Expand the network"}
          aria-expanded={open}
          onClick={toggle}
        >
          <ChevronDown className={cn("size-4 transition-transform", !open && "-rotate-90")} />
        </Button>
      </div>
      {open && (
        <div className="space-y-3 px-4 pb-4">
          {error ? (
            <p className="text-xs text-muted-foreground">The mesh is unavailable.</p>
          ) : isLoading || !flow ? (
            <Skeleton className="h-40" />
          ) : (
            <>
              <FlowGraph flow={flow} />
              <RestingSummary flow={flow} />
            </>
          )}
          <PhaseStepper />
        </div>
      )}
    </section>
  );
}

// The full-size view. It is the lobby's because the lobby is where a reader who
// has not picked a seat yet is standing, and this is the one screen that shows
// what no seat can see.
export function NetworkView() {
  const { data: flow, isLoading, error, refetch } = useNetworkFlow();
  const [wire, setWire] = useState<string | null>(null);

  const shown = useMemo(() => {
    if (!flow) return [];
    if (!wire) return flow.crossings;
    const { subscriber, host } = wireEnds(wire);
    return flow.crossings.filter(
      (c) =>
        (c.from === subscriber && c.to === host) || (c.from === host && c.to === subscriber),
    );
  }, [flow, wire]);

  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  if (isLoading || !flow) return <Skeleton className="h-72" />;

  return (
    <div className="space-y-4">
      {/* The drawing is the hero and the two lists sit under it, because a
          reader looks at the topology first and reads what crossed second. */}
      <div className="space-y-2 rounded-lg border p-3">
        <FlowGraph
          flow={flow}
          onSelectWire={setWire}
          selectedWire={wire}
          className="max-h-80"
        />
        <RestingSummary flow={flow} />
      </div>

      <div className="grid items-start gap-x-6 gap-y-4 md:grid-cols-2">
        <div className="space-y-2">
          <h3 className="border-b pb-1.5 text-xs font-medium text-muted-foreground">
            Step the day
          </h3>
          <PhaseStepper />
        </div>

        <div>
          <div className="flex items-baseline justify-between gap-2 border-b pb-1.5">
            <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
              {wire ? "Files on this wire" : "Files that crossed"}
              <Hint id="bulk-file" />
            </h3>
            {wire && (
              <Button variant="ghost" size="sm" onClick={() => setWire(null)}>
                Every wire
              </Button>
            )}
          </div>
          <CrossingList
            crossings={shown}
            emptyLabel={
              wire
                ? "Nothing has crossed this wire."
                : "Nothing has crossed yet. Step a phase, or advance the day."
            }
          />
        </div>
      </div>
    </div>
  );
}

// How many files are waiting, and on how many wires. It is stated in words as
// well as drawn because a dot the reader has not learnt to read yet is not an
// answer, and this is the state the whole view exists to show.
function RestingSummary({ flow }: { flow: NetworkFlow }) {
  const waiting = flow.crossings.filter(isResting).length;
  const wires = restingByWire(flow.crossings, flow.wires).size;
  if (waiting === 0) {
    return <p className="text-xs text-muted-foreground">Every file has been collected.</p>;
  }
  return (
    <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <span>
        <span className="font-medium text-foreground tabular-nums">{waiting}</span>
        {waiting === 1 ? " file waiting on " : " files waiting on "}
        <span className="tabular-nums">{wires}</span>
        {wires === 1 ? " wire" : " wires"}
      </span>
      <Hint id="download-queue" />
    </p>
  );
}

// Whether the push channel is open. A mesh that has stopped being told and one
// where nothing is happening look identical, so the difference is said.
function LiveDot() {
  const { live } = useNetworkWatch();
  return (
    <span
      className="flex size-2 shrink-0 rounded-full"
      title={live ? "Live — this updates as files move" : "Not connected — reload to reconnect"}
      aria-label={live ? "Live" : "Not connected"}
    >
      <span
        className={cn(
          "size-2 rounded-full",
          live ? "bg-success animate-pulse" : "bg-muted-foreground/40",
        )}
      />
    </span>
  );
}
