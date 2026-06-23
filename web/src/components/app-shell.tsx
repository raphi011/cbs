"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { usePanelRef, useDefaultLayout } from "react-resizable-panels";
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
  PanelLeftOpen,
  PanelLeftClose,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import { ParticipantSwitcher } from "./participant-switcher";
import { ThemeToggle } from "./theme-toggle";
import {
  ConceptPanelProvider,
  useConceptPanel,
} from "./concept-panel-provider";
import { ConceptPanelBody } from "./concept-panel";
import { ResetButton } from "./reset-button";
import { useIsDesktop } from "./use-is-desktop";

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

function NavLinks({
  collapsed,
  onNavigate,
}: {
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
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
            // Native tooltip + accessible name when icon-only; no shadcn tooltip dep.
            title={collapsed ? label : undefined}
            aria-label={collapsed ? label : undefined}
            className={cn(
              "flex items-center rounded-md text-sm font-medium transition-colors",
              collapsed ? "justify-center px-0 py-2" : "gap-2.5 px-3 py-2",
              active
                ? "bg-accent text-accent-foreground"
                : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
            )}
          >
            <Icon className="size-4 shrink-0" />
            {!collapsed && label}
          </Link>
        );
      })}
    </nav>
  );
}

function Brand({ collapsed }: { collapsed?: boolean }) {
  if (collapsed) {
    return (
      <Link
        href="/"
        title="Ledger"
        aria-label="Ledger — Core banking explorer"
        className="flex size-8 items-center justify-center rounded-md text-base font-semibold tracking-tight"
      >
        L
      </Link>
    );
  }
  return (
    <Link href="/" className="flex flex-col gap-0.5 px-3 py-1">
      <span className="text-base font-semibold tracking-tight">Ledger</span>
      <span className="text-xs text-muted-foreground">
        Core banking explorer
      </span>
    </Link>
  );
}

// Left nav panel content: brand + links + reset + a collapse toggle. Driven by
// the panel's collapsed state; when collapsed everything renders icon-only.
function NavSidebar({
  collapsed,
  onToggle,
}: {
  collapsed: boolean;
  onToggle: () => void;
}) {
  return (
    <div className="flex h-full flex-col border-r bg-card">
      <div
        className={cn(
          "flex h-14 items-center border-b",
          collapsed && "justify-center",
        )}
      >
        <Brand collapsed={collapsed} />
      </div>
      <div className={cn("flex-1 overflow-y-auto", collapsed ? "px-2 py-3" : "p-3")}>
        <NavLinks collapsed={collapsed} />
      </div>
      <div className="border-t py-3">
        <ResetButton collapsed={collapsed} />
        <div className={cn("mt-2 flex", collapsed ? "justify-center px-2" : "px-3")}>
          <Button
            variant="ghost"
            size={collapsed ? "icon" : "sm"}
            onClick={onToggle}
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            className={cn(!collapsed && "w-full justify-start gap-2")}
          >
            {collapsed ? (
              <PanelLeftOpen className="size-4" />
            ) : (
              <>
                <PanelLeftClose className="size-4" />
                Collapse
              </>
            )}
          </Button>
        </div>
      </div>
    </div>
  );
}

// Collapsed right rail: a thin clickable strip that re-expands the concepts panel.
function ConceptStrip({ onExpand }: { onExpand: () => void }) {
  return (
    <button
      type="button"
      onClick={onExpand}
      aria-label="Expand concept panel"
      title="Concepts"
      className="flex h-full w-full flex-col items-center gap-2 border-l bg-card py-3 text-muted-foreground hover:text-foreground"
    >
      <PanelRightOpen className="size-4" />
      <span className="[writing-mode:vertical-rl] text-xs">Concepts</span>
    </button>
  );
}

// Mobile nav: the desktop sidebar's links inside a left-side Sheet.
function MobileNavSheet() {
  return (
    <Sheet>
      <SheetTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Open navigation">
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
  );
}

// Mobile concepts: same body, opened by the topbar trigger or any `?`/link.
function ConceptSheet() {
  const { mobileOpen, setMobileOpen } = useConceptPanel();
  return (
    <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
      <SheetContent side="right" className="w-full max-w-sm p-0">
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

// Shared topbar. On mobile it grows the nav Sheet trigger, a brand wordmark and
// the concepts trigger; on desktop the panels own nav/concepts so it's just the
// participant switcher + theme toggle.
function Topbar({ mobile = false }: { mobile?: boolean }) {
  return (
    <header className="flex h-14 items-center gap-2 border-b px-4">
      {mobile && <MobileNavSheet />}
      {mobile && <span className="font-semibold">Ledger</span>}
      <div className="ml-auto flex items-center gap-2">
        <ParticipantSwitcher />
        {mobile && <ConceptTrigger />}
        <ThemeToggle />
      </div>
    </header>
  );
}

const NAV_COLLAPSED_KEY = "nav-collapsed";

// Desktop: three resizable panels (nav | main | concepts). Panel widths persist
// via useDefaultLayout. The concepts panel stays provider-owned (bridged
// below); the nav mirrors that pattern with its own persisted flag.
function DesktopShell({ children }: { children: React.ReactNode }) {
  const { collapsed, setCollapsed } = useConceptPanel();
  const conceptRef = usePanelRef();
  const navRef = usePanelRef();
  // Desktop-only component (gated by useIsDesktop), so localStorage is safe in
  // the initializer — it never runs on the server.
  const [navCollapsed, setNavCollapsed] = useState(
    () =>
      typeof localStorage !== "undefined" &&
      localStorage.getItem(NAV_COLLAPSED_KEY) === "true",
  );
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "app-shell",
    panelIds: ["nav", "main", "concepts"],
  });
  // Panel DOM elements, observed for width below. collapsedRef holds the
  // always-current `collapsed` so the observer compares without a stale closure.
  const navElRef = useRef<HTMLDivElement>(null);
  const conceptElRef = useRef<HTMLDivElement>(null);
  const collapsedRef = useRef(collapsed);
  useEffect(() => {
    collapsedRef.current = collapsed;
  }, [collapsed]);

  // Each panel's collapse is flag-driven (navCollapsed / the provider's
  // `collapsed`); these effects bridge the flag onto the imperative panel
  // handle. A toggle/`?`/strip flips the flag and the panel follows; at mount
  // the flag (restored from storage) makes the panel adopt the saved state.
  useEffect(() => {
    const panel = conceptRef.current;
    if (!panel) return;
    if (collapsed && !panel.isCollapsed()) panel.collapse();
    else if (!collapsed && panel.isCollapsed()) panel.expand();
  }, [collapsed, conceptRef]);

  useEffect(() => {
    const panel = navRef.current;
    if (!panel) return;
    if (navCollapsed && !panel.isCollapsed()) panel.collapse();
    else if (!navCollapsed && panel.isCollapsed()) panel.expand();
  }, [navCollapsed, navRef]);

  // Reverse direction (panel → flag) so a direct drag of a handle past its min
  // also flips the content mode. Observe the elements' rendered width rather
  // than rrp's onResize, which has awkward semantics around collapse snapping
  // and imperative calls — a ResizeObserver reflects the real width whatever
  // caused the change. The first (initial) callback is skipped so it can't
  // clobber the storage-restored flag before the bridge effects above have
  // settled the panel widths.
  useEffect(() => {
    const navEl = navElRef.current;
    const conceptEl = conceptElRef.current;
    if (!navEl || !conceptEl) return;
    let initial = true;
    const ro = new ResizeObserver(() => {
      if (initial) {
        initial = false;
        return;
      }
      setNavCollapsed((prev) => {
        const next = navEl.offsetWidth <= 64;
        return prev === next ? prev : next;
      });
      const c = conceptEl.offsetWidth <= 40;
      if (c !== collapsedRef.current) setCollapsed(c);
    });
    ro.observe(navEl);
    ro.observe(conceptEl);
    return () => ro.disconnect();
  }, [setCollapsed]);

  useEffect(() => {
    localStorage.setItem(NAV_COLLAPSED_KEY, String(navCollapsed));
  }, [navCollapsed]);

  return (
    <ResizablePanelGroup
      orientation="horizontal"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel
        id="nav"
        collapsible
        collapsedSize={56}
        minSize={200}
        maxSize={360}
        defaultSize={240}
        panelRef={navRef}
        elementRef={navElRef}
      >
        <NavSidebar
          collapsed={navCollapsed}
          // Flip the flag; the bridge effect drives the panel.
          onToggle={() => setNavCollapsed((prev) => !prev)}
        />
      </ResizablePanel>

      <ResizableHandle withHandle />

      <ResizablePanel id="main" minSize={480}>
        <div className="flex h-full min-w-0 flex-col">
          <Topbar />
          <main className="min-w-0 flex-1 overflow-y-auto p-4 md:p-8">
            {children}
          </main>
        </div>
      </ResizablePanel>

      <ResizableHandle withHandle />

      <ResizablePanel
        id="concepts"
        collapsible
        collapsedSize={32}
        minSize={256}
        maxSize={640}
        defaultSize={320}
        panelRef={conceptRef}
        elementRef={conceptElRef}
      >
        {collapsed ? (
          <ConceptStrip onExpand={() => setCollapsed(false)} />
        ) : (
          <div className="flex h-full flex-col border-l bg-card">
            <Suspense fallback={null}>
              <ConceptPanelBody onCollapse={() => setCollapsed(true)} />
            </Suspense>
          </div>
        )}
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}

// Mobile: the page scrolls as a whole; nav and concepts live in Sheets.
function MobileShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col">
      <Topbar mobile />
      <main className="min-w-0 flex-1 p-4">{children}</main>
      <ConceptSheet />
    </div>
  );
}

// Pick the layout by viewport. The desktop PanelGroup is mounted only when it
// can measure itself (never under SSR or `display:none`); see useIsDesktop.
// The wrapper gives the group a definite `100vh` height: rrp's Group hard-codes
// an inline `height:100%`, which only resolves against a definite-height
// ancestor (body's `min-height` doesn't count), so without this the shell
// collapses to its content height instead of filling the viewport.
function Shell({ children }: { children: React.ReactNode }) {
  const desktop = useIsDesktop();
  return desktop ? (
    <div className="h-screen overflow-hidden">
      <DesktopShell>{children}</DesktopShell>
    </div>
  ) : (
    <MobileShell>{children}</MobileShell>
  );
}

export function AppShell({ children }: { children: React.ReactNode }) {
  return (
    <ConceptPanelProvider>
      <Shell>{children}</Shell>
    </ConceptPanelProvider>
  );
}
