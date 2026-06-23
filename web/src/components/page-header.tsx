"use client";

import { Hint } from "./hint";
import type { HintKey } from "./hint-content";

interface PageHeaderProps {
  title: string;
  description?: React.ReactNode;
  hint?: HintKey;
  // Right-aligned actions (e.g. a "New" button).
  actions?: React.ReactNode;
}

// Standard page title block with an optional "?" hint and an actions slot.
export function PageHeader({
  title,
  description,
  hint,
  actions,
}: PageHeaderProps) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="space-y-1">
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          {hint && <Hint id={hint} />}
        </div>
        {description && (
          <p className="max-w-prose text-sm text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}
