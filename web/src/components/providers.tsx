"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "next-themes";
import { useState } from "react";

import { Toaster } from "@/components/ui/sonner";

// App-wide client providers: next-themes for light/dark, TanStack Query for
// server state, sonner for toasts. ThemeProvider is outermost because the
// Toaster reads the active theme via useTheme(). The QueryClient is created
// once per client via useState so it survives re-renders but isn't shared
// across requests on the server.
export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // The in-memory backend has no rate limits and resets on restart;
            // short stale time keeps the teaching UI feeling live.
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
        {children}
        <Toaster richColors closeButton />
      </QueryClientProvider>
    </ThemeProvider>
  );
}
