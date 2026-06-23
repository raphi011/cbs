"use client";

import { useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";

interface ConfirmActionProps {
  trigger: React.ReactNode;
  title: string;
  description?: React.ReactNode;
  confirmLabel?: string;
  destructive?: boolean;
  pending?: boolean;
  // Optional free-text field (e.g. a reversal description or reject reason),
  // passed to onConfirm.
  input?: { label: string; placeholder?: string; required?: boolean };
  // Should return a promise so the dialog closes only on success.
  onConfirm: (text: string) => void | Promise<void>;
}

// Generic confirmation dialog reused for reversal, reject, return, revoke,
// close and settle — actions that are easy to fire and hard to undo.
export function ConfirmAction({
  trigger,
  title,
  description,
  confirmLabel = "Confirm",
  destructive,
  pending,
  input,
  onConfirm,
}: ConfirmActionProps) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const blocked = (input?.required && !text.trim()) || pending;

  async function handle() {
    if (input?.required && !text.trim()) return;
    try {
      await onConfirm(text.trim());
      setOpen(false);
      setText("");
    } catch {
      // Caller surfaces the error via toast; keep the dialog open.
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {input && (
          <div className="space-y-2">
            <Label htmlFor="confirm-input">{input.label}</Label>
            <Textarea
              id="confirm-input"
              value={text}
              placeholder={input.placeholder}
              onChange={(e) => setText(e.target.value)}
            />
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            variant={destructive ? "destructive" : "default"}
            disabled={blocked}
            onClick={handle}
          >
            {pending ? "Working…" : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
