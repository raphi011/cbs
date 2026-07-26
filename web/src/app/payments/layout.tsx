"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

// Tabs for the network-wide payment screens: the payment list and the network's
// own audit trail. A single payment's detail page is a drill-down rather than a
// sibling section, so the tabs are hidden there — matching the participant
// section, where the tabs name sections and not every page below them.
const TABS = [
  { href: "/payments", label: "Payments" },
  { href: "/payments/audit", label: "Audit" },
];

export default function PaymentsLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const pathname = usePathname();
  const isSection = TABS.some((t) => t.href === pathname);

  if (!isSection) {
    return <>{children}</>;
  }

  return (
    <div className="space-y-6">
      <nav className="flex gap-1 border-b">
        {TABS.map((t) => (
          <Link
            key={t.href}
            href={t.href}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
              pathname === t.href
                ? "border-foreground text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </Link>
        ))}
      </nav>
      {children}
    </div>
  );
}
