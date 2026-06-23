"use client";

import { useEffect } from "react";
import { useParams, useRouter } from "next/navigation";
import { Plus } from "lucide-react";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { useParticipants } from "@/lib/api/hooks";
import { CreateParticipantDialog } from "./create-participant-dialog";

const LS_KEY = "ledger.lastParticipant";

// Topbar control to pick the active participant. The selection is reflected in
// the URL (/participants/[pid]/…) and remembered in localStorage so a reload
// keeps you in context.
export function ParticipantSwitcher() {
  const { data } = useParticipants();
  const router = useRouter();
  const params = useParams();
  const current = typeof params.pid === "string" ? params.pid : undefined;

  useEffect(() => {
    if (current) localStorage.setItem(LS_KEY, current);
  }, [current]);

  return (
    <div className="flex items-center gap-2">
      <Select
        value={current ?? ""}
        onValueChange={(pid) => router.push(`/participants/${pid}`)}
      >
        <SelectTrigger className="w-[180px]" size="sm">
          <SelectValue placeholder="Select participant" />
        </SelectTrigger>
        <SelectContent>
          {data?.map((p) => (
            <SelectItem key={p.id} value={p.id}>
              {p.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <CreateParticipantDialog
        trigger={
          <Button variant="outline" size="icon" aria-label="New participant">
            <Plus className="size-4" />
          </Button>
        }
      />
    </div>
  );
}
