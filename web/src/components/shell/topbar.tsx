"use client";

import Link from "next/link";
import { BookOpen } from "lucide-react";

import { Button } from "@/components/ui/button";
import { BusinessDay } from "@/components/business-day";
import { ScenarioPicker } from "@/components/scenario-picker";
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

// Shared topbar. On mobile it always grows the nav trigger (mobileSidebar,
// already wrapped in a Sheet by ShellFrame), a brand wordmark (link to /),
// and the concepts trigger — the sidebar itself is hidden behind the sheet,
// so nothing else on the page is holding a way back to the lobby. On
// desktop, the wordmark appears here too whenever there is no sidebar
// (`showBrand`, set by ShellFrame from whether it was given one): a shell
// with a sidebar renders its own `Brand` there and must not get a second
// wordmark, but a sidebar-less shell — the lobby, Learn, a customer's
// account — has nowhere else to put one, and the ruling is that the lobby is
// always one click away from every shell. Either way this is also the business
// date, the identity picker and the theme toggle, sitting here rather than in
// the sidebar so they are reachable from every shell, sidebar or not.
//
// The business date is here for a stronger reason than reachability: every date
// on every screen is the deployment's, about a year behind the wall clock, so a
// shell that did not show which day it is on would be read wrong. See
// BusinessDay.
//
// And the scenario picker beside it, for a stronger reason still: a deployment
// boots holding four banks and no customers, so until somebody runs a scenario
// every screen in the app is empty. See ScenarioPicker.
export function Topbar({
  mobile = false,
  mobileSidebar,
  showBrand = false,
}: {
  mobile?: boolean;
  mobileSidebar?: React.ReactNode;
  // True when this shell has no sidebar of its own to hold the brand
  // wordmark. Mobile always shows it regardless (the sidebar, if any, is
  // behind the sheet, not on screen), so the two conditions are combined
  // below rather than `showBrand` needing to account for `mobile` itself.
  showBrand?: boolean;
}) {
  return (
    <header className="flex h-14 items-center gap-2 border-b border-t-2 border-t-[color:var(--identity-accent,transparent)] px-4">
      {mobile && mobileSidebar}
      {(mobile || showBrand) && (
        <Link
          href="/"
          className="font-semibold text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          Ledger
        </Link>
      )}
      {/* `min-w-0` so the cluster is allowed to shrink at all: without it the
          row is sized by its contents and simply overflows the viewport rather
          than letting the identity label truncate. */}
      <div className="ml-auto flex min-w-0 items-center gap-2">
        <ScenarioPicker />
        <BusinessDay />
        {mobile && <ConceptTrigger />}
        <IdentityPicker />
        <ThemeToggle />
      </div>
    </header>
  );
}
