"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
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
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FieldLabel } from "./field-label";
import { useAddParticipant, useAssets } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Creates a participant (member bank). On success it navigates to the new
// participant's overview so the user lands in the right context.
export function CreateParticipantDialog({
  trigger,
}: {
  trigger?: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  // The assets the bank joins with. Each one provisions a suspense, reserve
  // and settlement account, and a bank can only hold money in an asset it
  // joined with — so this is the one screen that decides it, and it cannot be
  // changed afterwards. Defaults to the euro, matching the backend's default
  // for an omitted list.
  const [assets, setAssets] = useState<string[]>(["EUR"]);
  const known = useAssets();
  const router = useRouter();
  const add = useAddParticipant();

  function toggleAsset(code: string) {
    setAssets((prev) =>
      prev.includes(code) ? prev.filter((c) => c !== code) : [...prev, code],
    );
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed || assets.length === 0) return;
    add.mutate(
      { name: trimmed, assets },
      {
        onSuccess: (p) => {
          setOpen(false);
          setName("");
          setAssets(["EUR"]);
          toast.success(`Created ${p.name}`);
          router.push(`/participants/${p.id}`);
        },
        onError: (err) => toast.error(describeError(err)),
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button size="sm">
            <Plus className="size-4" />
            New participant
          </Button>
        )}
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>New participant</DialogTitle>
            <DialogDescription>
              A participant is a member bank in the network. Creating one
              provisions its chart of accounts: a customer subledger, a clearing
              suspense account, and a reserve account at the central bank.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="participant-name" required>
              Bank name
            </FieldLabel>
            <Input
              id="participant-name"
              value={name}
              autoFocus
              placeholder="e.g. Bank Alpha"
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel required>Assets</FieldLabel>
            <div className="flex flex-wrap gap-2">
              {(known.data ?? []).map((a) => {
                const on = assets.includes(a.code);
                return (
                  <Button
                    key={a.code}
                    type="button"
                    size="sm"
                    variant={on ? "default" : "outline"}
                    onClick={() => toggleAsset(a.code)}
                  >
                    {a.code}
                  </Button>
                );
              })}
            </div>
            <p className="text-xs text-muted-foreground">
              One suspense, reserve and settlement account is opened per asset.
              A bank can only hold money in an asset it joined with, and the set
              cannot be changed afterwards.
            </p>
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={add.isPending || !name.trim() || assets.length === 0}
            >
              {add.isPending ? "Creating…" : "Create participant"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
