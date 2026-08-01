"use client";

import Link from "next/link";
import { BookOpen } from "lucide-react";

import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/theme-toggle";
import { useConceptPanel } from "@/components/concept-panel-provider";
import { IdentityPicker } from "./identity-picker";

function ConceptTrigger() {
  const { togglePanel } = useConceptPanel();
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label="Open concepts"
      onClick={togglePanel}
    >
      <BookOpen className="size-5" />
    </Button>
  );
}

// Shared topbar. On mobile it grows the nav trigger (mobileSidebar, already
// wrapped in a Sheet by ShellFrame), a brand wordmark (link to /), and the
// concepts trigger; on desktop the sidebar owns the brand + nav and this is
// the identity picker plus the theme toggle. The brand wordmark is always a
// link to the lobby (/) — the root of the app and the way back from any
// persona console. The identity picker sits here, not in the sidebar, so it
// is reachable from every shell — including the sidebar-less PlainShell the
// lobby and Learn use.
export function Topbar({
  mobile = false,
  mobileSidebar,
}: {
  mobile?: boolean;
  mobileSidebar?: React.ReactNode;
}) {
  return (
    <header className="flex h-14 items-center gap-2 border-b border-t-2 border-t-[color:var(--identity-accent,transparent)] px-4">
      {mobile && mobileSidebar}
      {mobile && (
        <Link
          href="/"
          className="font-semibold text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          Ledger
        </Link>
      )}
      <div className="ml-auto flex items-center gap-2">
        {mobile && <ConceptTrigger />}
        <IdentityPicker />
        <ThemeToggle />
      </div>
    </header>
  );
}
