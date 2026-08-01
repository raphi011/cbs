"use client";

import { useIdentity } from "@/lib/identity";
import { ConsoleShell } from "./console-shell";
import { PlainShell } from "./plain-shell";

// Who you are decides which software you get. The central bank, the clearing
// house and a bank's back office are the same console with a different nav —
// one ShellFrame wiring, parameterised by identity — so they share
// ConsoleShell. The customer's shell does not: it has no left panel at all
// (a content column, like the lobby), which is a different arrangement, not
// just a different nav, so it will not join this group when it arrives in
// Task 12. Until then a customer URL has no page to render and falls through
// to the plain shell like the lobby does.
export function PersonaShell({ children }: { children: React.ReactNode }) {
  const identity = useIdentity();
  switch (identity?.persona) {
    case "central-bank":
    case "clearing-house":
    case "bank":
      return <ConsoleShell identity={identity}>{children}</ConsoleShell>;
    default:
      return <PlainShell>{children}</PlainShell>;
  }
}
