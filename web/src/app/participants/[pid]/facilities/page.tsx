"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ChevronRight } from "lucide-react";

import { ArrearsBadge } from "@/components/arrears-badge";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { IdText } from "@/components/id-text";
import { Money } from "@/components/money";
import { Skeleton } from "@/components/ui/skeleton";
import { OpenFacilityForm } from "@/components/forms/open-facility-form";
import { useAssetLookup, useFacilities } from "@/lib/api/hooks";
import { FACILITY_KIND_LABEL } from "@/lib/enums";
import type { Facility } from "@/lib/types";

// One row per facility. Mirrors DepositAccountRow: the asset is resolved from
// the network-wide lookup rather than a per-row fetch, since (unlike a
// deposit's balance) commitment/drawn/outstanding already ride on the list
// response.
function FacilityRow({ pid, facility }: { pid: string; facility: Facility }) {
  const { byCode } = useAssetLookup();
  const asset = byCode.get(facility.asset);

  return (
    <Link
      href={`/participants/${pid}/facilities/${facility.id}`}
      className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-sm font-medium">{facility.name}</span>
        <span className="text-xs text-muted-foreground">
          {FACILITY_KIND_LABEL[facility.kind]}
        </span>
        <IdText id={facility.id} />
      </span>
      <span className="flex items-center gap-4">
        {asset ? (
          <>
            <span className="text-right text-sm">
              <Money amount={facility.commitment} asset={asset} />
              <span className="block text-xs font-normal text-muted-foreground">
                commitment
              </span>
            </span>
            <span className="text-right text-sm font-medium">
              <Money amount={facility.drawn} asset={asset} />
              <span className="block text-xs font-normal text-muted-foreground">
                drawn
              </span>
            </span>
            <span className="text-right text-sm font-medium">
              <Money amount={facility.outstanding} asset={asset} />
              <span className="block text-xs font-normal text-muted-foreground">
                outstanding
              </span>
            </span>
          </>
        ) : (
          <Skeleton className="h-8 w-40" />
        )}
        <ArrearsBadge
          bucket={facility.arrearsBucket}
          nonPerforming={facility.nonPerforming}
          showHint={false}
        />
        <EnumBadge value={facility.status} />
        <ChevronRight className="size-4 text-muted-foreground" />
      </span>
    </Link>
  );
}

export default function FacilitiesPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useFacilities(pid);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Facilities
          <Hint id="credit-facility" />
        </h2>
        <OpenFacilityForm pid={pid} />
      </div>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : isLoading ? (
        <Skeleton className="h-32 w-full" />
      ) : data && data.length > 0 ? (
        <div className="divide-y rounded-lg border">
          {data.map((f) => (
            <FacilityRow key={f.id} pid={pid} facility={f} />
          ))}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          No credit facilities yet. Open a term loan or revolving line to
          extend credit against a customer.
        </p>
      )}
    </div>
  );
}
