"use client";

import { AlertCircle } from "lucide-react";

import { Button } from "@/components/ui/button";
import { describeError } from "@/lib/api/errors";

// Inline error block for failed queries: a category-aware message plus an
// optional retry. Mutations use toasts instead (see forms).
export function ErrorState({
  error,
  onRetry,
}: {
  error: unknown;
  onRetry?: () => void;
}) {
  return (
    <div className="flex flex-col items-start gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-4">
      <div className="flex items-start gap-2.5">
        <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" />
        <p className="text-sm text-foreground">{describeError(error)}</p>
      </div>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  );
}
