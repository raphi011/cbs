"use client";

import { homeFor, navFor, type Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { SidebarNav } from "./sidebar-nav";
import { Topbar } from "./topbar";

const IDENTITY: Identity = { persona: "clearing-house" };

export function ClearingHouseShell({ children }: { children: React.ReactNode }) {
  const items = navFor(IDENTITY);
  const home = homeFor(IDENTITY);
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
