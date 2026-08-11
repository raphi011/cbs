"use client";

import { accentFor } from "@/lib/accent";
import type { Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { Topbar } from "./topbar";

// A retail bank app is a content column, not a console: no left panel, no
// sections, and a narrow column. That is what makes the switch unmistakable —
// and it is a shell rather than a variant of the console because the layouts
// genuinely differ, not because a flag was cheaper.
//
// There is nothing to navigate between: an account is one page, and `navFor`
// returns an empty list for this persona to say so.
//
// The concepts rail stays. A real retail app has no concepts rail, so this costs
// a little realism and buys the thing the repository exists for; the max-w-2xl
// constraint is on the content column, not the viewport, so the panel keeps its
// own width and its collapse strip.
//
// The accent is the customer's *bank's*. You are a customer of Aurora, and the
// screen should say so without a label.
export function CustomerShell({
  pid,
  did,
  children,
}: {
  pid: string;
  did: string;
  children: React.ReactNode;
}) {
  const identity: Identity = { persona: "customer", pid, did };

  return (
    <ShellFrame accent={accentFor(identity)} topbar={<Topbar />}>
      <div className="mx-auto w-full max-w-2xl space-y-6">{children}</div>
    </ShellFrame>
  );
}
