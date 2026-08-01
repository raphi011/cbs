"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";

import { cn } from "@/lib/utils";
import type { NavItem } from "@/lib/identity";
import { Button } from "@/components/ui/button";
import { ResetButton } from "@/components/reset-button";

function NavLinks({
  items,
  collapsed,
  onNavigate,
}: {
  items: NavItem[];
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
  const pathname = usePathname();
  return (
    <nav className="flex flex-col gap-0.5">
      {items.map(({ href, label, icon: Icon, exact }) => {
        const active = exact ? pathname === href : pathname.startsWith(href);
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
        className="flex size-8 items-center justify-center rounded-md text-base font-semibold tracking-tight text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-md"
      >
        L
      </Link>
    );
  }
  return (
    <Link
      href="/"
      className="flex flex-col gap-0.5 px-3 py-1 text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-md"
    >
      <span className="text-base font-semibold tracking-tight">Ledger</span>
      <span className="text-xs text-muted-foreground">
        Core banking explorer
      </span>
    </Link>
  );
}

// A persona's sidebar: brand (link to /) + links + reset + a collapse toggle.
// The brand wordmark is always a link to the lobby — the root of the app and
// the way back from any persona console. When the sidebar is collapsed,
// everything renders icon-only. Driven by the panel's collapsed state.
export function SidebarNav({
  items,
  collapsed,
  onToggle,
  onNavigate,
}: {
  items: NavItem[];
  collapsed: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
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
        <NavLinks items={items} collapsed={collapsed} onNavigate={onNavigate} />
      </div>
      <div className="border-t py-3">
        {/* Resetting the system is the central bank's act, and ResetButton
            already addresses cb("/admin/reset") explicitly, which is correct
            rather than awkward wherever you happen to be standing. */}
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
