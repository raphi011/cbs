"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";
import { accentFor } from "@/lib/accent";
import { navFor, type Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { Topbar } from "./topbar";

// A retail bank app is a content column, not a console: no left panel, a top tab
// strip, and a narrow column. That is what makes the switch unmistakable — and it
// is a shell rather than a variant of the console because the layouts genuinely
// differ, not because a flag was cheaper.
//
// The concepts rail stays. A real retail app has no concepts rail, so this costs
// a little realism and buys the thing the repository exists for; the max-w-2xl
// constraint is on the content column, not the viewport, so the panel keeps its
// own width and its collapse strip.
//
// The accent is the customer's *bank's*. You are a customer of Aurora, and the
// screen should say so without a label.
export function CustomerShell({
  pid,
  did,
  children,
}: {
  pid: string;
  did: string;
  children: React.ReactNode;
}) {
  const identity: Identity = { persona: "customer", pid, did };
  const pathname = usePathname();
  const items = navFor(identity);

  return (
    <ShellFrame accent={accentFor(identity)} topbar={<Topbar />}>
      <div className="mx-auto w-full max-w-2xl space-y-6">
        <nav className="flex gap-1 border-b">
          {items.map((t) => {
            const active = t.exact ? pathname === t.href : pathname.startsWith(t.href);
            return (
              <Link
                key={t.href}
                href={t.href}
                className={cn(
                  "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
                  active
                    ? "border-foreground text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground",
                )}
              >
                {t.label}
              </Link>
            );
          })}
        </nav>
        {children}
      </div>
    </ShellFrame>
  );
}
