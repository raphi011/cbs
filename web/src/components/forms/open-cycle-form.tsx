"use client";

import { useState } from "react";
import { Plus } from "lucide-react";
import { toast } from "sonner";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { FieldLabel } from "@/components/field-label";
import { useOpenCycle, useSchemes } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Opens a clearing cycle for a scheme. Payments under that scheme accumulate
// into the cycle; closing it computes net positions, settling moves the money.
export function OpenCycleForm() {
  const [open, setOpen] = useState(false);
  const schemes = useSchemes();
  const [scheme, setScheme] = useState("");
  const create = useOpenCycle();

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!scheme) return;
    try {
      const c = await create.mutateAsync({ scheme });
      toast.success(`Cycle opened (${c.id})`);
      setScheme("");
      setOpen(false);
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus className="size-4" />
          Open cycle
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Open clearing cycle</DialogTitle>
            <DialogDescription>
              Collects this scheme&apos;s payments so they can be netted and
              settled together.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="cycle-scheme" hint="clearing-vs-settlement" required>
              Scheme
            </FieldLabel>
            <Select value={scheme} onValueChange={setScheme}>
              <SelectTrigger id="cycle-scheme">
                <SelectValue placeholder="Choose a scheme…" />
              </SelectTrigger>
              <SelectContent>
                {schemes.data?.map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.id} · {s.settlementModel}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button type="submit" disabled={create.isPending || !scheme}>
              {create.isPending ? "Opening…" : "Open cycle"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
