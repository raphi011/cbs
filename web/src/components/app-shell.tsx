"use client";

import { Suspense } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  ArrowLeftRight,
  Building2,
  FileSignature,
  LayoutDashboard,
  Menu,
  Network,
  RefreshCw,
  Landmark,
  BookOpen,
  PanelRightOpen,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { ParticipantSwitcher } from "./participant-switcher";
import { ThemeToggle } from "./theme-toggle";
import {
  ConceptPanelProvider,
  useConceptPanel,
} from "./concept-panel-provider";
import { ConceptPanelBody } from "./concept-panel";
import { ResetButton } from "./reset-button";

interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
}

// Network-wide screens (no participant selected). Payments, mandates, cycles
// and settlements are global because each spans two participants.
const NETWORK_NAV: NavItem[] = [
  { href: "/", label: "Dashboard", icon: LayoutDashboard },
  { href: "/payments", label: "Payments", icon: ArrowLeftRight },
  { href: "/mandates", label: "Mandates", icon: FileSignature },
  { href: "/cycles", label: "Clearing cycles", icon: RefreshCw },
  { href: "/settlements", label: "Settlements", icon: Landmark },
  { href: "/central-bank", label: "Central bank", icon: Building2 },
  { href: "/schemes", label: "Schemes", icon: Network },
];

function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  return (
    <nav className="flex flex-col gap-0.5">
      {NETWORK_NAV.map(({ href, label, icon: Icon }) => {
        const active = href === "/" ? pathname === "/" : pathname.startsWith(href);
        return (
          <Link
            key={href}
            href={href}
            onClick={onNavigate}
            className={cn(
              "flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium transition-colors",
              active
                ? "bg-accent text-accent-foreground"
                : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
            )}
          >
            <Icon className="size-4 shrink-0" />
            {label}
          </Link>
        );
      })}
    </nav>
  );
}

function Brand() {
  return (
    <Link href="/" className="flex flex-col gap-0.5 px-3 py-1">
      <span className="text-base font-semibold tracking-tight">Ledger</span>
      <span className="text-xs text-muted-foreground">
        Core banking explorer
      </span>
    </Link>
  );
}

// Desktop right rail: full panel when expanded, a thin clickable strip when
// collapsed. Hidden below md (the mobile sheet takes over).
function ConceptRail() {
  const { collapsed, setCollapsed } = useConceptPanel();

  if (collapsed) {
    return (
      <button
        type="button"
        onClick={() => setCollapsed(false)}
        aria-label="Expand concept panel"
        className="hidden w-8 shrink-0 flex-col items-center gap-2 border-l bg-card py-3 text-muted-foreground hover:text-foreground md:flex"
      >
        <PanelRightOpen className="size-4" />
        <span className="[writing-mode:vertical-rl] text-xs">Concepts</span>
      </button>
    );
  }

  return (
    <aside className="hidden w-80 shrink-0 flex-col border-l bg-card md:flex">
      <Suspense fallback={null}>
        <ConceptPanelBody onCollapse={() => setCollapsed(true)} />
      </Suspense>
    </aside>
  );
}

// Mobile sheet: same body, opened by the topbar trigger or any `?`/link.
function ConceptSheet() {
  const { mobileOpen, setMobileOpen } = useConceptPanel();
  return (
    <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
      <SheetContent side="right" className="w-full max-w-sm p-0 md:hidden">
        <SheetTitle className="sr-only">Concept explanation</SheetTitle>
        <Suspense fallback={null}>
          <ConceptPanelBody />
        </Suspense>
      </SheetContent>
    </Sheet>
  );
}

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

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen">
      {/* Desktop sidebar */}
      <aside className="hidden w-60 shrink-0 flex-col border-r bg-card md:flex">
        <div className="flex h-14 items-center border-b">
          <Brand />
        </div>
        <div className="flex-1 overflow-y-auto p-3">
          <NavLinks />
        </div>
        <div className="border-t py-3">
          <ResetButton />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center gap-2 border-b px-4">
          <Sheet>
            <SheetTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Open navigation"
                className="md:hidden"
              >
                <Menu className="size-5" />
              </Button>
            </SheetTrigger>
            <SheetContent side="left" className="w-64 p-0">
              <SheetTitle className="sr-only">Navigation</SheetTitle>
              <div className="flex h-14 items-center border-b">
                <Brand />
              </div>
              <div className="p-3">
                <NavLinks />
              </div>
              <div className="border-t py-3">
                <ResetButton />
              </div>
            </SheetContent>
          </Sheet>
          <span className="font-semibold md:hidden">Ledger</span>
          <div className="ml-auto flex items-center gap-2">
            <ParticipantSwitcher />
            <ConceptTrigger />
            <ThemeToggle />
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4 md:p-8">{children}</main>
      </div>

      <ConceptRail />
      <ConceptSheet />
    </div>
  );
}

export function AppShell({ children }: { children: React.ReactNode }) {
  return (
    <ConceptPanelProvider>
      <Shell>{children}</Shell>
    </ConceptPanelProvider>
  );
}
