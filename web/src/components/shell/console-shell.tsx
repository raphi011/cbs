"use client";

import { navFor, type Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { SidebarNav } from "./sidebar-nav";
import { Topbar } from "./topbar";

// The central bank, the clearing house and a member bank are the same
// software with a different nav: one ShellFrame/SidebarNav/Topbar wiring,
// parameterised by the identity that decides which items it shows. A pid
// distinguishes one bank from another the same way it distinguishes one
// persona from the next.
export function ConsoleShell({
  identity,
  children,
}: {
  identity: Identity;
  children: React.ReactNode;
}) {
  const items = navFor(identity);
  return (
    <ShellFrame
      topbar={<Topbar />}
      sidebar={(collapsed, toggle) => (
        <SidebarNav items={items} collapsed={collapsed} onToggle={toggle} />
      )}
      mobileSidebar={
        <SidebarNav items={items} collapsed={false} onToggle={() => {}} />
      }
    >
      {children}
    </ShellFrame>
  );
}
