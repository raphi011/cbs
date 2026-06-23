# Concept Side Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-`?` Radix popover with a persistent, markdown-rendering right-side concept panel whose open concept is deep-linkable via a `?concept=` URL param and whose concepts cross-link via wiki-style `[[key]]` syntax.

**Architecture:** A `ConceptPanelProvider` (mounted in `AppShell`) holds the page's default concept, the collapse/mobile-open UI state, and an `openConcept(key)` action that pushes `?concept=key`. Only the panel reads `useSearchParams` (isolated behind `<Suspense>`); `openConcept` reads `window.location.search` in its click handler so other query params are preserved without forcing a Suspense boundary around the app. Each page's primary concept is registered through the `hint` prop already on `PageHeader`.

**Tech Stack:** Next.js 16 (App Router) · React 19 · Tailwind v4 · shadcn/Radix · `react-markdown` + `remark-gfm`.

**Working directory:** all paths are relative to `web/`. All commands run from `web/`.

**Verification reality:** there is no frontend test runner and Playwright is not installed. The hard gates are `npm run typecheck`, `npm run lint`, and `npm run build` (all must be clean). Behavioral checks are manual browser drives against a running backend (`go run ./cmd/server` from repo root, then `npm run dev`). Steps below use these gates in place of unit tests.

**Commit prefix:** this is the personal `raphi011/core-banking-system` repo — commits do **not** take a Jira prefix.

---

## File structure

**New files (`src/components/`):**
- `concept-links.ts` — pure helpers: `preprocessConceptMarkdown`, `parseConceptLinks`, `validateConceptContent`. No React.
- `concept-panel-provider.tsx` — context: default concept, collapse + mobile-open state, `openConcept`/`closeConcept`/`togglePanel`.
- `concept-markdown.tsx` — `react-markdown` wrapper: `[[ ]]` preprocessing + custom `<a>` renderer.
- `concept-panel.tsx` — the right-rail / sheet UI. Reads `useSearchParams`.

**Modified files:**
- `src/components/app-shell.tsx` — mount provider, render rail + mobile sheet + topbar trigger.
- `src/components/field-label.tsx` — delete dead `hintTitle`/`hintBody` props.
- `src/components/hint.tsx` — drop Popover; button calls `openConcept(id)`.
- `src/components/page-header.tsx` — register default concept from `hint`.
- `src/components/hint-content.ts` — `body` becomes rich markdown with `[[ ]]` links.

**Task order rationale:** the panel pipeline is built bottom-up (helpers → provider → renderer → panel) so the app keeps compiling. `FieldLabel` is simplified (Task 7) *before* `Hint` is rewritten (Task 8) because both currently share the ad-hoc `Hint` API; cleaning `FieldLabel` first keeps every intermediate task green. Content expansion comes last so each rewritten concept can be eyeballed in the finished panel.

---

## Task 1: Add markdown dependencies

**Files:**
- Modify: `web/package.json` (via npm)

- [ ] **Step 1: Install**

Run (from `web/`):
```bash
npm install react-markdown@9 remark-gfm@4
```

- [ ] **Step 2: Verify the build still passes**

Run: `npm run build`
Expected: build completes with no errors.

- [ ] **Step 3: Commit**

```bash
git add package.json package-lock.json
git commit -m "Add react-markdown and remark-gfm"
```

---

## Task 2: Concept link helpers

Pure, framework-free string functions. `validateConceptContent` is the dev-time guard that makes broken `[[ ]]` links fail loudly.

**Files:**
- Create: `src/components/concept-links.ts`

- [ ] **Step 1: Write the module**

```ts
import { hintContent, type HintKey } from "./hint-content";

// Matches [[key]] and [[key|custom label]].
const LINK_RE = /\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g;

// Rewrite wiki-links to standard markdown links with a `concept:` scheme so
// react-markdown renders them and our custom <a> can intercept them. The label
// defaults to the target concept's title.
export function preprocessConceptMarkdown(body: string): string {
  return body.replace(LINK_RE, (_match, rawKey: string, label?: string) => {
    const key = rawKey.trim();
    const text = (label ?? hintContent[key as HintKey]?.title ?? key).trim();
    return `[${text}](concept:${key})`;
  });
}

// Distinct, valid concept keys referenced by a body — used for the "Related" row.
export function parseConceptLinks(body: string): HintKey[] {
  const keys = new Set<HintKey>();
  for (const match of body.matchAll(LINK_RE)) {
    const key = match[1].trim();
    if (key in hintContent) keys.add(key as HintKey);
  }
  return [...keys];
}

// Dev-time guard: throws if any body links to a key that isn't in the registry.
export function validateConceptContent(): void {
  const broken: string[] = [];
  for (const [key, entry] of Object.entries(hintContent)) {
    for (const match of entry.body.matchAll(LINK_RE)) {
      const target = match[1].trim();
      if (!(target in hintContent)) broken.push(`${key} → [[${target}]]`);
    }
  }
  if (broken.length > 0) {
    throw new Error(`Unknown concept links:\n${broken.join("\n")}`);
  }
}
```

- [ ] **Step 2: Verify types**

Run: `npm run typecheck`
Expected: clean (no errors). `hintContent` currently has short string bodies — the helpers compile against them fine.

- [ ] **Step 3: Commit**

```bash
git add src/components/concept-links.ts
git commit -m "Add concept link parsing and validation helpers"
```

---

## Task 3: Concept panel provider

**Files:**
- Create: `src/components/concept-panel-provider.tsx`

- [ ] **Step 1: Write the provider**

```tsx
"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";
import { usePathname, useRouter } from "next/navigation";

import type { HintKey } from "./hint-content";
import { validateConceptContent } from "./concept-links";

const STORAGE_KEY = "concept-panel-collapsed";

interface ConceptPanelContextValue {
  defaultConcept: HintKey | null;
  setDefaultConcept: (key: HintKey | null) => void;
  collapsed: boolean;
  setCollapsed: (collapsed: boolean) => void;
  mobileOpen: boolean;
  setMobileOpen: (open: boolean) => void;
  openConcept: (key: HintKey) => void;
  closeConcept: () => void;
  togglePanel: () => void;
}

const ConceptPanelContext = createContext<ConceptPanelContextValue | null>(null);

export function useConceptPanel(): ConceptPanelContextValue {
  const ctx = useContext(ConceptPanelContext);
  if (!ctx) {
    throw new Error("useConceptPanel must be used within ConceptPanelProvider");
  }
  return ctx;
}

export function ConceptPanelProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [defaultConcept, setDefaultConcept] = useState<HintKey | null>(null);
  const [collapsed, setCollapsedState] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);

  // Hydrate collapse preference after mount to avoid an SSR/client mismatch.
  useEffect(() => {
    setCollapsedState(localStorage.getItem(STORAGE_KEY) === "true");
  }, []);

  // Fail loudly in dev if any concept body links to an unknown key.
  useEffect(() => {
    if (process.env.NODE_ENV !== "production") validateConceptContent();
  }, []);

  const setCollapsed = useCallback((next: boolean) => {
    setCollapsedState(next);
    localStorage.setItem(STORAGE_KEY, String(next));
  }, []);

  // Read live params from the URL in the handler (not during render) so we
  // preserve any other query params and avoid a Suspense boundary on the app.
  const openConcept = useCallback(
    (key: HintKey) => {
      const params = new URLSearchParams(window.location.search);
      params.set("concept", key);
      router.push(`${pathname}?${params.toString()}`);
      setMobileOpen(true);
    },
    [router, pathname],
  );

  const closeConcept = useCallback(() => {
    const params = new URLSearchParams(window.location.search);
    params.delete("concept");
    const query = params.toString();
    router.push(query ? `${pathname}?${query}` : pathname);
    setMobileOpen(false);
  }, [router, pathname]);

  const togglePanel = useCallback(() => {
    setCollapsed(false);
    setMobileOpen(true);
  }, [setCollapsed]);

  return (
    <ConceptPanelContext.Provider
      value={{
        defaultConcept,
        setDefaultConcept,
        collapsed,
        setCollapsed,
        mobileOpen,
        setMobileOpen,
        openConcept,
        closeConcept,
        togglePanel,
      }}
    >
      {children}
    </ConceptPanelContext.Provider>
  );
}
```

- [ ] **Step 2: Verify types**

Run: `npm run typecheck`
Expected: clean. (Provider is unused so far — that's fine.)

- [ ] **Step 3: Commit**

```bash
git add src/components/concept-panel-provider.tsx
git commit -m "Add concept panel provider with URL-synced open action"
```

---

## Task 4: Concept markdown renderer

**Files:**
- Create: `src/components/concept-markdown.tsx`

- [ ] **Step 1: Write the renderer**

```tsx
"use client";

import Link from "next/link";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

import { cn } from "@/lib/utils";
import { useConceptPanel } from "./concept-panel-provider";
import { preprocessConceptMarkdown } from "./concept-links";
import type { HintKey } from "./hint-content";

// Renders a concept body as markdown. `concept:` links swap the panel; internal
// paths use next/link; everything else opens in a new tab.
export function ConceptMarkdown({ body }: { body: string }) {
  const { openConcept } = useConceptPanel();
  const source = preprocessConceptMarkdown(body);

  return (
    <div
      className={cn(
        "text-sm leading-relaxed text-muted-foreground",
        "[&_p]:mb-3 [&_strong]:font-medium [&_strong]:text-foreground",
        "[&_ul]:mb-3 [&_ul]:list-disc [&_ul]:pl-5 [&_li]:mb-1",
        "[&_h3]:mb-1.5 [&_h3]:mt-4 [&_h3]:text-sm [&_h3]:font-semibold [&_h3]:text-foreground",
        "[&_pre]:my-3 [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:font-mono [&_pre]:text-xs [&_pre]:text-foreground",
        "[&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-xs",
        "[&_pre_code]:bg-transparent [&_pre_code]:p-0",
      )}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a({ href, children }) {
            if (href?.startsWith("concept:")) {
              const key = href.slice("concept:".length) as HintKey;
              return (
                <button
                  type="button"
                  onClick={() => openConcept(key)}
                  className="text-primary underline decoration-dotted underline-offset-2 hover:decoration-solid"
                >
                  {children}
                </button>
              );
            }
            if (href?.startsWith("/")) {
              return (
                <Link href={href} className="text-primary underline underline-offset-2">
                  {children}
                </Link>
              );
            }
            return (
              <a
                href={href}
                target="_blank"
                rel="noreferrer"
                className="text-primary underline underline-offset-2"
              >
                {children}
              </a>
            );
          },
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
}
```

- [ ] **Step 2: Verify types**

Run: `npm run typecheck`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add src/components/concept-markdown.tsx
git commit -m "Add concept markdown renderer with cross-link interception"
```

---

## Task 5: Concept panel UI

The panel is URL-driven, so the browser back/forward buttons already walk concept history — there is no separate in-panel back control, only a collapse button.

**Files:**
- Create: `src/components/concept-panel.tsx`

- [ ] **Step 1: Write the panel**

```tsx
"use client";

import { useSearchParams } from "next/navigation";
import { PanelRightClose } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useConceptPanel } from "./concept-panel-provider";
import { ConceptMarkdown } from "./concept-markdown";
import { parseConceptLinks } from "./concept-links";
import { hintContent, type HintKey } from "./hint-content";

// Resolves the concept to show: the URL's `concept` param if valid, else the
// page default. Returns null when neither is set.
function useActiveConcept(): HintKey | null {
  const searchParams = useSearchParams();
  const { defaultConcept } = useConceptPanel();
  const fromUrl = searchParams.get("concept");
  if (fromUrl && fromUrl in hintContent) return fromUrl as HintKey;
  return defaultConcept;
}

// The panel body — shared by the desktop rail and the mobile sheet.
export function ConceptPanelBody({ onCollapse }: { onCollapse?: () => void }) {
  const { openConcept } = useConceptPanel();
  const active = useActiveConcept();

  if (!active) {
    return (
      <div className="p-4 text-sm text-muted-foreground">
        Select a “?” to see an explanation here.
      </div>
    );
  }

  const entry = hintContent[active];
  const related = parseConceptLinks(entry.body).filter((k) => k !== active);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <h2 className="flex-1 text-sm font-semibold">{entry.title}</h2>
        {onCollapse && (
          <Button
            variant="ghost"
            size="icon"
            aria-label="Collapse concept panel"
            onClick={onCollapse}
          >
            <PanelRightClose className="size-4" />
          </Button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <ConceptMarkdown body={entry.body} />
        {related.length > 0 && (
          <div className="mt-4 border-t pt-3">
            <p className="mb-2 text-xs text-muted-foreground">Related</p>
            <div className="flex flex-wrap gap-2">
              {related.map((key) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => openConcept(key)}
                  className="rounded-full border px-2.5 py-0.5 text-xs text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground"
                >
                  {hintContent[key].title}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Verify types**

Run: `npm run typecheck`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add src/components/concept-panel.tsx
git commit -m "Add concept panel body with related-concept chips"
```

---

## Task 6: Wire the panel into AppShell

This makes the panel live: a collapsible right rail on `md+`, a strip when collapsed, a right sheet on mobile, and a topbar trigger. The panel body is wrapped in `<Suspense>` because it reads `useSearchParams`.

**Files:**
- Modify: `src/components/app-shell.tsx`

- [ ] **Step 1: Replace the file**

```tsx
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

interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
}

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

function ResetNote() {
  return (
    <p className="px-3 text-xs leading-relaxed text-muted-foreground">
      Data lives in memory and resets when the backend restarts.
    </p>
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
          <ResetNote />
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
                <ResetNote />
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
```

- [ ] **Step 2: Verify types, lint, build**

Run: `npm run typecheck && npm run lint && npm run build`
Expected: all clean.

- [ ] **Step 3: Manual browser check**

Start backend (`go run ./cmd/server` from repo root) and `npm run dev`. Then:
- On a wide window, the right rail shows on every page. With no `?concept=` and no page default yet, it reads “Select a ‘?’ to see an explanation here.”
- The collapse button shrinks the rail to the vertical “Concepts” strip; clicking the strip expands it; the choice survives a page navigation (localStorage).
- Narrow the window below `md`: the rail disappears; the topbar book icon opens the right sheet.

- [ ] **Step 4: Commit**

```bash
git add src/components/app-shell.tsx
git commit -m "Render concept panel as collapsible rail and mobile sheet"
```

---

## Task 7: Remove dead ad-hoc hint props

`hintTitle`/`hintBody` on `FieldLabel` have no call sites (verified by grep) and the rewritten `Hint` (Task 8) will not support them. Removing them here first keeps Task 8 green. `FieldLabel` keeps using `<Hint id={hint} />`, which the current `Hint` still supports — so this task compiles on its own.

**Files:**
- Modify: `src/components/field-label.tsx`

- [ ] **Step 1: Replace the file**

```tsx
"use client";

import { Label } from "@/components/ui/label";
import { Hint } from "./hint";
import type { HintKey } from "./hint-content";

interface FieldLabelProps {
  htmlFor?: string;
  children: React.ReactNode;
  hint?: HintKey;
  required?: boolean;
}

// A form label that can carry a "?" hint inline.
export function FieldLabel({
  htmlFor,
  children,
  hint,
  required,
}: FieldLabelProps) {
  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>
        {children}
        {required && <span className="text-destructive"> *</span>}
      </Label>
      {hint && <Hint id={hint} />}
    </div>
  );
}
```

- [ ] **Step 2: Verify types, lint, build**

Run: `npm run typecheck && npm run lint && npm run build`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add src/components/field-label.tsx
git commit -m "Drop unused ad-hoc hint props from FieldLabel"
```

---

## Task 8: Point Hint at the panel

**Files:**
- Modify: `src/components/hint.tsx`

- [ ] **Step 1: Replace the file**

```tsx
"use client";

import { HelpCircle } from "lucide-react";

import { cn } from "@/lib/utils";
import { useConceptPanel } from "./concept-panel-provider";
import { hintContent, type HintKey } from "./hint-content";

interface HintProps {
  id: HintKey;
  className?: string;
}

// A small "?" button that opens the referenced concept in the side panel. It
// owns its click (preventDefault/stopPropagation) so it's safe inside links and
// clickable rows.
export function Hint({ id, className }: HintProps) {
  const { openConcept } = useConceptPanel();
  const title = hintContent[id].title;

  return (
    <button
      type="button"
      aria-label={`Explain: ${title}`}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        openConcept(id);
      }}
      className={cn(
        "inline-flex size-4 shrink-0 items-center justify-center rounded-full align-middle text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
        className,
      )}
    >
      <HelpCircle className="size-3.5" />
    </button>
  );
}
```

- [ ] **Step 2: Verify types, lint, build**

Run: `npm run typecheck && npm run lint && npm run build`
Expected: clean. (All `<Hint>` call sites pass `id=`; `FieldLabel` was simplified in Task 7.)

- [ ] **Step 3: Manual browser check**

In the running app, click any `?`. The referenced concept opens in the rail (or sheet on mobile) and the URL gains `?concept=<key>`.

- [ ] **Step 4: Commit**

```bash
git add src/components/hint.tsx
git commit -m "Open Hint concepts in the side panel instead of a popover"
```

---

## Task 9: Register page default concept

**Files:**
- Modify: `src/components/page-header.tsx`

- [ ] **Step 1: Replace the file**

```tsx
"use client";

import { useEffect } from "react";

import { Hint } from "./hint";
import { useConceptPanel } from "./concept-panel-provider";
import type { HintKey } from "./hint-content";

interface PageHeaderProps {
  title: string;
  description?: React.ReactNode;
  hint?: HintKey;
  // Right-aligned actions (e.g. a "New" button).
  actions?: React.ReactNode;
}

// Standard page title block. Its `hint` both renders the inline "?" and
// registers the page's default concept for the side panel.
export function PageHeader({
  title,
  description,
  hint,
  actions,
}: PageHeaderProps) {
  const { setDefaultConcept } = useConceptPanel();

  useEffect(() => {
    setDefaultConcept(hint ?? null);
    return () => setDefaultConcept(null);
  }, [hint, setDefaultConcept]);

  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="space-y-1">
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          {hint && <Hint id={hint} />}
        </div>
        {description && (
          <p className="max-w-prose text-sm text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}
```

- [ ] **Step 2: Verify types, lint, build**

Run: `npm run typecheck && npm run lint && npm run build`
Expected: clean.

- [ ] **Step 3: Manual browser check**

Load `/` with no query param: the rail shows the Dashboard's default concept (`clearing-vs-settlement`, set by `PageHeader hint="clearing-vs-settlement"`). Navigate to `/payments`: the default updates to that page's hint. Hard-load `/payments?concept=netting`: the rail opens directly on “Netting”.

- [ ] **Step 4: Commit**

```bash
git add src/components/page-header.tsx
git commit -m "Register page default concept from PageHeader hint"
```

---

## Task 10: Confirm the Popover primitive is not orphaned by a stray import

- [ ] **Step 1: Check for remaining Popover usages**

Run: `grep -rln "components/ui/popover\|PopoverTrigger" src`
Expected: `hint.tsx` no longer appears. List any other files.

- [ ] **Step 2: Decide**

`hint.tsx` was the only consumer. Per CLAUDE.md, do **not** delete the shared `src/components/ui/popover.tsx` shadcn primitive — leave it for future reuse. This task is a no-op confirmation; only act if Step 1 reveals a stray `popover` import introduced by this work.

---

## Task 11: Expand concept content to rich markdown

Rewrite every `body` in `src/components/hint-content.ts` from a one-paragraph string into rich markdown distilled from `README.md`, cross-linked with `[[ ]]`. `HintEntry`’s shape does not change.

**Authoring rules:**
- 2–5 short paragraphs. Lead with the one-sentence definition, then mechanism, then an example.
- Use `**bold**` for the term being defined and key quantities.
- Use a fenced code block for any worked number example (renders as the monospace box).
- Link related concepts inline with `[[key]]` or `[[key|label]]`. Every link target must be an existing key (the dev guard from Task 2 throws otherwise).
- Keep the existing `title` values unchanged.

**Exemplars (author the rest to match):**

```ts
  netting: {
    title: "Netting",
    body: `Customers move money by **gross** amounts, but banks settle only the **net**.

If A→B is €300 and B→A is €100, only **€200** of reserves moves at [[clearing-vs-settlement|settlement]].

\`\`\`
A net: −200 (pays in)
B net: +200 (receives)
──────────
sum:    0  ✓
\`\`\`

[[net-positions|Net positions]] always sum to zero across participants, so the settlement transaction balances under [[double-entry]].`,
  },
  "double-entry": {
    title: "Double-entry bookkeeping",
    body: `Every transaction has equal **debits** and **credits**, so money never appears or disappears — it only moves between accounts.

If debits don't equal credits, the transaction is rejected. Which side increases an account depends on its [[normal-balance]].

This invariant is what lets a [[reversal]] cleanly undo a posting and what makes [[netting]] sum to zero.`,
  },
  "clearing-vs-settlement": {
    title: "Clearing vs. settlement",
    body: `**Clearing** is exchanging and netting instructions — agreeing who owes whom. No central-bank money moves.

**Settlement** is the actual movement of [[reserve-account|reserves]] between banks at the [[central-bank-reserves|central bank]] — the moment of finality.

A [[netting|clearing cycle]] nets many payments; settlement then moves each bank's single [[net-positions|net position]].`,
  },
```

Each step below converts one domain group, then verifies and commits. After each, run `npm run typecheck` and open a couple of the rewritten concepts in the running app — the dev guard throws on any bad `[[ ]]` key; fix what it reports.

- [ ] **Step 1: Ledger fundamentals**

Keys: `double-entry`, `normal-balance`, `ledger-vs-subledger`, `amount-cents`, `idempotency-key`, `reversal`, `booking-date`, `value-date`. Source: `README.md` ledger section.

```bash
git add src/components/hint-content.ts
git commit -m "Expand ledger-fundamentals concepts to rich markdown"
```

- [ ] **Step 2: Account types**

Keys: `account-type-asset`, `account-type-liability`, `account-type-equity`, `account-type-revenue`, `account-type-expense`, `account-status`. Cross-link to `[[normal-balance]]` where natural. Source: `README.md` account-types section.

```bash
git add src/components/hint-content.ts
git commit -m "Expand account-type concepts to rich markdown"
```

- [ ] **Step 3: Balances & holds**

Keys: `balance-book`, `balance-holds`, `balance-available`, `overdraft`, `holds`, `hold-capture`, `hold-release`. Source: `README.md` balances/holds section.

```bash
git add src/components/hint-content.ts
git commit -m "Expand balance and hold concepts to rich markdown"
```

- [ ] **Step 4: Schemes & mandates**

Keys: `scheme-direction-push`, `scheme-direction-pull`, `requires-mandate`, `allows-return`, `settlement-delay`, `mandate`, `settlement-model-net`, `settlement-model-gross`. Source: `README.md` schemes/mandates section.

```bash
git add src/components/hint-content.ts
git commit -m "Expand scheme and mandate concepts to rich markdown"
```

- [ ] **Step 5: Payments & settlement**

Keys: `payment-lifecycle`, `debtor-leg`, `creditor-leg`, `clearing-vs-settlement`, `netting`, `net-positions`, `reserve-account`, `central-bank-reserves`, `clearing-suspense`. Source: `README.md` payments/settlement section.

```bash
git add src/components/hint-content.ts
git commit -m "Expand payment and settlement concepts to rich markdown"
```

- [ ] **Step 6: Audit**

Keys: `audit-trail`, `snapshot`. Source: `README.md` audit section.

```bash
git add src/components/hint-content.ts
git commit -m "Expand audit concepts to rich markdown"
```

---

## Task 12: Final verification

- [ ] **Step 1: Gates**

Run: `npm run typecheck && npm run lint && npm run build`
Expected: all clean.

- [ ] **Step 2: Behavioral matrix (manual, against a running backend)**

Confirm each:
- Click a `?` → panel shows the concept; URL gains `?concept=<key>`.
- Click an inline `[[link]]` → panel swaps; URL updates.
- Browser **back** returns to the previous concept; **forward** re-applies it.
- Hard-load `/payments?concept=netting` → opens on “Netting”.
- A worked-example fenced block renders as a monospace box.
- "Related" chips reflect the body's links and switch concepts on click.
- Below `md`: the book icon opens the sheet; `?`/links open it too.
- Collapse the rail, navigate pages → it stays collapsed.
- No console errors (the dev link-validation guard stays silent).

- [ ] **Step 3: Final commit (if any cleanup remains)**

```bash
git add -A
git commit -m "Finalize concept side panel"
```
