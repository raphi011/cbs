"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "next-themes";
import { useState } from "react";

import { Toaster } from "@/components/ui/sonner";
import { NetworkWatcher } from "@/components/network/network-watcher";

// App-wide client providers: next-themes for light/dark, TanStack Query for
// server state, sonner for toasts. ThemeProvider is outermost because the
// Toaster reads the active theme via useTheme(). The QueryClient is created
// once per client via useState so it survives re-renders but isn't shared
// across requests on the server.
//
// NetworkWatcher is inside the QueryClientProvider and outside every route,
// which is what makes it ONE subscription: the mesh is drawn on the lobby and
// in the rail of every other shell, and a connection per drawing would be two
// connections watching one deployment.
export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // The backend is a local teaching tool with no rate limits, so a
            // short stale time is cheap and keeps the UI feeling live.
            staleTime: 5_000,
            retry: false,
            refetchOnWindowFocus: false,
          },
        },
      }),
  );

  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
      <QueryClientProvider client={client}>
        <NetworkWatcher>{children}</NetworkWatcher>
        <Toaster richColors closeButton />
      </QueryClientProvider>
    </ThemeProvider>
  );
}
