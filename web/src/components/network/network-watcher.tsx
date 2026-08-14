"use client";

import { createContext, useContext, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { networkEventsPath } from "@/lib/api/endpoints";
import type { DayProblem } from "@/lib/types";

// The push channel, and the only subscriber on it in this browser.
//
// # Why one, mounted at the root
//
// The mesh is drawn in the rail on every shell and full size on the lobby, and
// a connection per drawing would be two connections showing one deployment. So
// the subscription is mounted once, above every route, and what the drawings
// read is this context.
//
// # Why an event does not refetch the mesh
//
// A mesh read is every row every institution holds, and a phase moves many
// files at once — a refetch per event would pay that read once per movement.
// The events therefore only say THAT something moved; a single trailing refetch
// says what. One phase is one burst and one refetch.
//
// # Why a reconnect refetches
//
// What a watcher missed while it was away is not replayed, deliberately: a
// dropped watcher is one that fell too far behind, and the whole picture is
// worth more to it than the holes. So re-opening reads the snapshot again.

// How long the stream stays quiet before the snapshot is read. Long enough that
// a phase's files are one burst, short enough to read as immediate.
const SETTLE_MS = 250;

interface NetworkWatchValue {
  // Whether the channel is open. A drawing says so, because a mesh that has
  // stopped updating and one where nothing is happening look identical.
  live: boolean;
}

const NetworkWatchContext = createContext<NetworkWatchValue>({ live: false });

export function useNetworkWatch(): NetworkWatchValue {
  return useContext(NetworkWatchContext);
}

export function NetworkWatcher({ children }: { children: React.ReactNode }) {
  const qc = useQueryClient();
  const [live, setLive] = useState(false);
  // Held in a ref rather than state: a burst must restart the timer without
  // re-rendering every screen behind the rail once per file.
  const settle = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const source = new EventSource(networkEventsPath);

    // Whether this connection has ever been open, which is what separates a
    // reconnect (read the snapshot again) from the first open (the drawing has
    // already read it).
    let opened = false;

    const refresh = () => {
      if (settle.current) clearTimeout(settle.current);
      settle.current = setTimeout(() => {
        // Every query, not the mesh alone: a file that moved cleared a payment,
        // moved a reserve or booked a statement, and naming the subset one
        // event touches would be naming the whole of the domain.
        void qc.invalidateQueries();
      }, SETTLE_MS);
    };

    source.onopen = () => {
      setLive(true);
      if (opened) void qc.invalidateQueries();
      opened = true;
    };

    // EventSource reconnects on its own, so an error is "not live for now"
    // rather than a failure to report. A backend that is down produces one of
    // these every few seconds and no toast, which is correct: the screens in
    // front of it are already saying so.
    source.onerror = () => setLive(false);

    source.addEventListener("file", refresh);
    source.addEventListener("outcome", refresh);
    // How far the day has got. It is its own event because end of day moves no
    // file, so a watcher told only about movements would never hear that the day
    // had got past it.
    source.addEventListener("phase", refresh);
    // A problem is a file an institution could not process, and there is nobody
    // to answer: the sender was told its file arrived and went away. So it is
    // said out loud rather than counted.
    source.addEventListener("problem", (e) => {
      const problem = parse<DayProblem>(e);
      if (problem) toast.error(`${problem.institution} could not process a file`, {
        description: problem.detail,
      });
      refresh();
    });

    return () => {
      if (settle.current) clearTimeout(settle.current);
      source.close();
    };
  }, [qc]);

  return (
    <NetworkWatchContext.Provider value={{ live }}>{children}</NetworkWatchContext.Provider>
  );
}

// parse reads one event's payload. A frame this browser cannot read is dropped
// rather than thrown from a listener the stream would then stop delivering to.
function parse<T>(e: Event): T | null {
  try {
    return JSON.parse((e as MessageEvent<string>).data) as T;
  } catch {
    return null;
  }
}
