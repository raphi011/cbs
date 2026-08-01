"use client";

import { homeFor, navFor, type Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { SidebarNav } from "./sidebar-nav";
import { Topbar } from "./topbar";

export function BankShell({
  pid,
  children,
}: {
  pid: string;
  children: React.ReactNode;
}) {
  const identity: Identity = { persona: "bank", pid };
  const items = navFor(identity);
  const home = homeFor(identity);
  return (
    <ShellFrame
      topbar={<Topbar />}
      sidebar={(collapsed, toggle) => (
        <SidebarNav items={items} home={home} collapsed={collapsed} onToggle={toggle} />
      )}
      mobileSidebar={
        <SidebarNav items={items} home={home} collapsed={false} onToggle={() => {}} />
      }
    >
      {children}
    </ShellFrame>
  );
}
