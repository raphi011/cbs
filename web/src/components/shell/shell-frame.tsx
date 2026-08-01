"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { usePanelRef, useDefaultLayout } from "react-resizable-panels";
import { Menu, PanelRightOpen } from "lucide-react";

import {
  Sheet,
  SheetContent,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import {
  useConceptPanel,
} from "@/components/concept-panel-provider";
import { ConceptPanelBody } from "@/components/concept-panel";
import { useIsDesktop } from "@/components/use-is-desktop";
import { Topbar } from "./topbar";

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

// Mobile nav: an arbitrary sidebar's content (a persona's SidebarNav) inside a
// left-side Sheet, opened by the topbar's hamburger trigger.
function MobileNavSheet({ children }: { children: React.ReactNode }) {
  return (
    <Sheet>
      <SheetTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Open navigation">
          <Menu className="size-5" />
        </Button>
      </SheetTrigger>
      <SheetContent side="left" className="w-64 p-0">
        <SheetTitle className="sr-only">Navigation</SheetTitle>
        {children}
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

const NAV_COLLAPSED_KEY = "nav-collapsed";

// Desktop: two or three resizable panels, depending on whether a sidebar was
// given. Panel widths persist via useDefaultLayout, under an id that differs
// between the two arrangements so a two-panel layout can never be restored
// into a three-panel group. The concepts panel stays provider-owned (bridged
// below); the nav mirrors that pattern with its own persisted flag when there
// is one.
function DesktopShell({
  children,
  sidebar,
  topbar,
  accent,
}: {
  children: React.ReactNode;
  sidebar?: (collapsed: boolean, toggle: () => void) => React.ReactNode;
  topbar: React.ReactNode;
  accent?: string;
}) {
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
  const { defaultLayout, onLayoutChanged } = useDefaultLayout(
    sidebar
      ? { id: "app-shell-nav", panelIds: ["nav", "main", "concepts"] }
      : { id: "app-shell-plain", panelIds: ["main", "concepts"] },
  );
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
  // settled the panel widths. There is no nav element to observe when there is
  // no sidebar; the concepts element is always observed.
  useEffect(() => {
    const navEl = navElRef.current;
    const conceptEl = conceptElRef.current;
    if (!conceptEl) return;
    let initial = true;
    const ro = new ResizeObserver(() => {
      if (initial) {
        initial = false;
        return;
      }
      if (navEl) {
        setNavCollapsed((prev) => {
          const next = navEl.offsetWidth <= 64;
          return prev === next ? prev : next;
        });
      }
      const c = conceptEl.offsetWidth <= 40;
      if (c !== collapsedRef.current) setCollapsed(c);
    });
    if (navEl) ro.observe(navEl);
    ro.observe(conceptEl);
    return () => ro.disconnect();
  }, [setCollapsed]);

  useEffect(() => {
    if (!sidebar) return;
    localStorage.setItem(NAV_COLLAPSED_KEY, String(navCollapsed));
  }, [navCollapsed, sidebar]);

  return (
    <div
      // rrp's Group hard-codes an inline `height:100%`, which only resolves
      // against a definite-height ancestor (body's `min-height` doesn't
      // count), so without this the shell collapses to its content height
      // instead of filling the viewport.
      className="h-screen overflow-hidden"
      style={
        accent ? ({ "--identity-accent": accent } as React.CSSProperties) : undefined
      }
    >
      <ResizablePanelGroup
        orientation="horizontal"
        defaultLayout={defaultLayout}
        onLayoutChanged={onLayoutChanged}
      >
        {sidebar && (
          <>
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
              {sidebar(navCollapsed, () => setNavCollapsed((prev) => !prev))}
            </ResizablePanel>

            <ResizableHandle withHandle />
          </>
        )}

        <ResizablePanel id="main" minSize={480}>
          <div className="flex h-full min-w-0 flex-col">
            {topbar}
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
    </div>
  );
}

// Mobile: the page scrolls as a whole; nav and concepts live in Sheets. There
// is no nav Sheet at all when there is no sidebar (the lobby, Learn).
function MobileShell({
  children,
  mobileSidebar,
  accent,
}: {
  children: React.ReactNode;
  mobileSidebar?: React.ReactNode;
  accent?: string;
}) {
  return (
    <div
      className="flex min-h-screen flex-col"
      style={
        accent ? ({ "--identity-accent": accent } as React.CSSProperties) : undefined
      }
    >
      <Topbar
        mobile
        mobileSidebar={mobileSidebar && <MobileNavSheet>{mobileSidebar}</MobileNavSheet>}
      />
      <main className="min-w-0 flex-1 p-4">{children}</main>
      <ConceptSheet />
    </div>
  );
}

export function ShellFrame({
  children,
  sidebar,
  mobileSidebar,
  topbar,
  accent,
}: {
  children: React.ReactNode;
  // Rendered inside the collapsible left panel, given the panel's collapsed
  // state. Absent means no left panel at all — the customer shell and the lobby,
  // which are content columns rather than consoles.
  sidebar?: (collapsed: boolean, toggle: () => void) => React.ReactNode;
  mobileSidebar?: React.ReactNode;
  topbar: React.ReactNode;
  // The identity's colour, set as --identity-accent on the outermost element of
  // both arrangements. Undefined outside the persona system.
  accent?: string;
}) {
  // Pick the layout by viewport. The desktop PanelGroup is mounted only when it
  // can measure itself (never under SSR or `display:none`); see useIsDesktop.
  const desktop = useIsDesktop();
  return desktop ? (
    <DesktopShell sidebar={sidebar} topbar={topbar} accent={accent}>
      {children}
    </DesktopShell>
  ) : (
    <MobileShell mobileSidebar={mobileSidebar} accent={accent}>
      {children}
    </MobileShell>
  );
}
