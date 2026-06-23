"use client";

import { useSyncExternalStore } from "react";

// Matches Tailwind's `md` breakpoint so the JS gate and the CSS responsive
// utilities agree on where "desktop" begins.
const DESKTOP_QUERY = "(min-width: 768px)";

function subscribe(callback: () => void): () => void {
  const mql = window.matchMedia(DESKTOP_QUERY);
  mql.addEventListener("change", callback);
  return () => mql.removeEventListener("change", callback);
}

function getSnapshot(): boolean {
  return window.matchMedia(DESKTOP_QUERY).matches;
}

// Server (and the initial hydration render) reports mobile; the client
// re-renders to the real value immediately after mount. A brief mobile-shell
// flash on desktop is the accepted cost of never server-rendering the panels.
function getServerSnapshot(): boolean {
  return false;
}

// True at/above the `md` breakpoint. Used to mount the desktop resizable
// PanelGroup only when it can actually measure itself — react-resizable-panels
// mis-measures panels inside a `display:none` subtree, so we gate on JS rather
// than hiding the group with CSS.
export function useIsDesktop(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
