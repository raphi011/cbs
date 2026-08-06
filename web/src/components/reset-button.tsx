"use client";

import { RotateCcw } from "lucide-react";
import { toast } from "sonner";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { useResetState } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Sidebar action: confirm, then reset the backend to the sample dataset.
//
// The copy says "all data" rather than "the in-memory state": the backend runs
// on an ephemeral database or on a file depending on how it was started, the
// frontend has no way to tell which, and against a file the wipe is durable. A
// reset has no undo either way, so this is a destructive confirm. When the nav
// rail is collapsed it renders icon-only with a native tooltip.
export function ResetButton({ collapsed }: { collapsed?: boolean }) {
  const reset = useResetState();
  return (
    <div className={cn("flex", collapsed ? "justify-center px-2" : "px-3")}>
      <ConfirmAction
        destructive
        title="Reset all data?"
        description="This permanently wipes all backend data and reloads the built-in sample dataset (banks, accounts, payments, clearing cycles and settlements). Anything you created will be lost, and there is no undo."
        confirmLabel="Reset data"
        pending={reset.isPending}
        onConfirm={async () => {
          try {
            await reset.mutateAsync();
            toast.success("Data reset to the sample dataset");
          } catch (e) {
            toast.error(describeError(e));
            throw e; // keep the dialog open on failure
          }
        }}
        trigger={
          collapsed ? (
            <Button
              variant="outline"
              size="icon"
              aria-label="Reset data"
              title="Reset data"
            >
              <RotateCcw className="size-4" />
            </Button>
          ) : (
            <Button variant="outline" size="sm" className="w-full justify-start gap-2">
              <RotateCcw className="size-4" />
              Reset data
            </Button>
          )
        }
      />
    </div>
  );
}
