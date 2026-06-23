"use client";

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
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { ParticipantSwitcher } from "./participant-switcher";
import { ThemeToggle } from "./theme-toggle";

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

function ResetNote() {
  return (
    <p className="px-3 text-xs leading-relaxed text-muted-foreground">
      Data lives in memory and resets when the backend restarts.
    </p>
  );
}

export function AppShell({ children }: { children: React.ReactNode }) {
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
        {/* Topbar: mobile nav drawer (small screens) + participant switcher */}
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
            <ThemeToggle />
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4 md:p-8">{children}</main>
      </div>
    </div>
  );
}
