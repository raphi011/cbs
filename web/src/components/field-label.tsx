"use client";

import { Label } from "@/components/ui/label";
import { Hint } from "./hint";
import type { HintKey } from "./hint-content";

interface FieldLabelProps {
  htmlFor?: string;
  children: React.ReactNode;
  // Attach a "?" hint by registry id or ad-hoc title+content.
  hint?: HintKey;
  hintTitle?: string;
  hintBody?: React.ReactNode;
  required?: boolean;
}

// A form label that can carry a "?" hint inline, so any field can be explained
// without cluttering the layout.
export function FieldLabel({
  htmlFor,
  children,
  hint,
  hintTitle,
  hintBody,
  required,
}: FieldLabelProps) {
  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>
        {children}
        {required && <span className="text-destructive"> *</span>}
      </Label>
      {hint ? (
        <Hint id={hint} />
      ) : hintTitle ? (
        <Hint title={hintTitle}>{hintBody}</Hint>
      ) : null}
    </div>
  );
}
