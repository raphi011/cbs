import { cn } from "@/lib/utils";

// Renders a backend ID (bank_1, 200.100.001, …) in monospace. IDs are
// everywhere in this system, so showing them consistently helps users follow
// how money moves between participants and accounts.
export function IdText({
  id,
  className,
}: {
  id: string;
  className?: string;
}) {
  return (
    <span
      className={cn("font-mono text-xs text-muted-foreground", className)}
    >
      {id}
    </span>
  );
}
