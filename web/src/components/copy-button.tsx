"use client";

import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";

import { Button } from "@/components/ui/button";

// Copies a value to the clipboard and says so for a moment. The confirmation is
// the icon itself rather than a toast: what was copied is still on screen beside
// it, so a banner would say less than the tick does.
export function CopyButton({
  value,
  label,
  className,
}: {
  value: string;
  // What is being copied, for a screen reader — "IBAN", not "copy".
  label: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  // The tick is a timer, so it has to be cleared when the button leaves before
  // it fires — a setState on an unmounted component otherwise.
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      className={className}
      aria-label={copied ? `${label} copied` : `Copy ${label}`}
      onClick={async () => {
        // Clipboard access is refused outside a secure context and can be denied
        // by permission; a copy that did not happen must not claim it did.
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
        } catch {
          setCopied(false);
        }
      }}
    >
      {copied ? <Check className="text-emerald-600" /> : <Copy />}
    </Button>
  );
}
