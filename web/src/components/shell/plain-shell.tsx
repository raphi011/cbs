"use client";

import { ShellFrame } from "./shell-frame";
import { Topbar } from "./topbar";

// No sidebar: a content column and the concepts rail, nothing else. This is
// the lobby's shell and Learn's — neither is a persona's console — and it
// falls out of useIdentity() returning null.
export function PlainShell({ children }: { children: React.ReactNode }) {
  return <ShellFrame topbar={<Topbar />}>{children}</ShellFrame>;
}
