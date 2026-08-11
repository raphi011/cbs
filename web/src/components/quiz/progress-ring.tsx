import { cn } from "@/lib/utils";

export function ProgressRing({
  pct,
  size = 44,
  stroke = 4,
  className,
  label,
  children,
}: {
  pct: number;
  size?: number;
  stroke?: number;
  className?: string;
  /**
   * What the ring means, for a reader who cannot see it. The visible children
   * are a compressed form ("3/20") that does not survive being read aloud
   * without the surrounding shape, so the label carries the sentence and the
   * children are hidden from the accessibility tree.
   */
  label?: string;
  children?: React.ReactNode;
}) {
  const r = (size - stroke) / 2;
  const circumference = 2 * Math.PI * r;
  const clamped = Math.max(0, Math.min(100, pct));
  const dash = (clamped / 100) * circumference;

  return (
    <div
      className={cn("relative inline-grid shrink-0 place-items-center", className)}
      style={{ width: size, height: size }}
      role={label ? "img" : undefined}
      aria-label={label}
    >
      <svg width={size} height={size} className="-rotate-90" aria-hidden="true" focusable="false">
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" strokeWidth={stroke} className="stroke-muted" />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          strokeWidth={stroke}
          // A round cap adds half a stroke width at each end, so on the first
          // question of twenty — an arc barely longer than the stroke is thick —
          // the caps are the whole mark and it reads as a detached pill floating
          // above the number rather than as the start of a ring.
          strokeLinecap={dash > stroke * 2 ? "round" : "butt"}
          strokeDasharray={`${dash} ${circumference - dash}`}
          className="stroke-primary transition-[stroke-dasharray] duration-500"
        />
      </svg>
      {children != null && (
        <span aria-hidden={label ? "true" : undefined} className="absolute text-xs font-semibold">
          {children}
        </span>
      )}
    </div>
  );
}
