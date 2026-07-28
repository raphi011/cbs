import { AlertTriangle } from "lucide-react";

import { Pill } from "@/components/enum-badge";
import { Hint } from "@/components/hint";
import { ARREARS_TONE, type ArrearsBucket } from "@/lib/enums";

// ArrearsBadge renders a facility's days-past-due bucket, colored from
// Current (good) through 90+ (bad) — see lending.ArrearsFor. `nonPerforming`
// is carried on the DTO as its own field rather than derived from the bucket
// string on the client (today the two always agree — NonPerforming is set at
// exactly 90 days — but the badge renders what the server actually sent
// rather than re-deriving one from the other). NonPerforming MARKS ONLY: it
// changes no accounting.
export function ArrearsBadge({
  bucket,
  nonPerforming,
  showHint = true,
}: {
  bucket: ArrearsBucket;
  nonPerforming: boolean;
  // Suppressed in a list row so the hint icon doesn't repeat once per row;
  // the detail page (one badge per screen) keeps it.
  showHint?: boolean;
}) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <Pill tone={ARREARS_TONE[bucket]}>{bucket}</Pill>
      {nonPerforming && (
        <Pill tone="bad">
          <AlertTriangle className="mr-1 size-3" />
          Non-performing
        </Pill>
      )}
      {showHint && <Hint id="arrears" />}
    </span>
  );
}
