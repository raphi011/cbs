"use client";

import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";

import { cn } from "@/lib/utils";

// Renders an opaque backend ID (bank_1, acct_9, …) in monospace with a
// click-to-copy button. IDs are everywhere in this system, so copying them for
// the next request is a constant need.
export function CopyId({
  id,
  className,
}: {
  id: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(id);
      setCopied(true);
      toast.success(`Copied ${id}`);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      toast.error("Couldn't copy to clipboard");
    }
  }

  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        void copy();
      }}
      title={`Copy ${id}`}
      className={cn(
        "inline-flex items-center gap-1 rounded font-mono text-xs text-muted-foreground transition-colors hover:text-foreground",
        className,
      )}
    >
      {id}
      {copied ? (
        <Check className="size-3 text-emerald-600" />
      ) : (
        <Copy className="size-3 opacity-60" />
      )}
    </button>
  );
}
