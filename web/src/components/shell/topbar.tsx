"use client";

import { BookOpen } from "lucide-react";

import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/theme-toggle";
import { useConceptPanel } from "@/components/concept-panel-provider";

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
// wrapped in a Sheet by ShellFrame), a brand wordmark and the concepts
// trigger; on desktop the panels own nav/concepts so it's just the theme
// toggle. Nothing sits where the participant switcher used to — Task 7 puts
// the identity picker there.
export function Topbar({
  mobile = false,
  mobileSidebar,
}: {
  mobile?: boolean;
  mobileSidebar?: React.ReactNode;
}) {
  return (
    <header className="flex h-14 items-center gap-2 border-b px-4">
      {mobile && mobileSidebar}
      {mobile && <span className="font-semibold">Ledger</span>}
      <div className="ml-auto flex items-center gap-2">
        {mobile && <ConceptTrigger />}
        <ThemeToggle />
      </div>
    </header>
  );
}
