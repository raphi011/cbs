"use client";

import { RotateCcw } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { useResetState } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Sidebar action: confirm, then reset the backend to the sample dataset. The
// in-memory backend has no undo, so this is a destructive confirm.
export function ResetButton() {
  const reset = useResetState();
  return (
    <div className="px-3">
      <ConfirmAction
        destructive
        title="Reset all data?"
        description="This wipes the in-memory state and reloads the built-in sample dataset (banks, accounts, payments, clearing cycles and settlements). Anything you created will be lost."
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
          <Button variant="outline" size="sm" className="w-full justify-start gap-2">
            <RotateCcw className="size-4" />
            Reset data
          </Button>
        }
      />
    </div>
  );
}
