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
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FieldLabel } from "./field-label";
import { useAddParticipant, useAssets } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Founds a bank and applies to the scheme for it.
//
// What POST /members answers with is a FOUNDED bank, and this form says so
// rather than reporting an admission. The application is answered by two other
// institutions and arrives afterwards, so at the moment this dialog closes the
// bank has its own book and its own customers and no settlement account
// anywhere: it cannot pay or be paid, and the clearing house's list is where it
// is seen becoming a member.
//
// It does not navigate to the new bank's console: a bank founded at runtime has
// no listener until the server restarts, so that console would 502 on every
// request — exactly what the lobby and identity picker refuse to walk into.
export function CreateParticipantDialog({
  trigger,
}: {
  trigger?: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  // The bank's ISO 9362 business identifier code — what a counterparty
  // addresses it by, and what the mesh routes on. Required: POST /members
  // 422s without one (payment.Participant.BIC).
  const [bic, setBic] = useState("");
  // The assets the bank joins with. Founding opens a suspense and a reserve
  // account per asset in the bank's own book, and the application asks the
  // central bank for a settlement account in each; a bank can only hold money in
  // an asset it joined with, so this is the one screen that decides it, and it
  // cannot be changed afterwards. Defaults to the euro, matching the backend's
  // default for an omitted list.
  const [assets, setAssets] = useState<string[]>(["EUR"]);
  const known = useAssets();
  const add = useAddParticipant();

  function toggleAsset(code: string) {
    setAssets((prev) =>
      prev.includes(code) ? prev.filter((c) => c !== code) : [...prev, code],
    );
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = name.trim();
    const trimmedBic = bic.trim();
    if (!trimmed || !trimmedBic || assets.length === 0) return;
    add.mutate(
      { name: trimmed, bic: trimmedBic, assets },
      {
        onSuccess: (p) => {
          setOpen(false);
          setName("");
          setBic("");
          setAssets(["EUR"]);
          // Reports what was DONE and not what was granted. The bank exists
          // and its application is out; whether the scheme takes it is answered
          // elsewhere, and a toast saying "admitted" would be this screen
          // claiming an outcome no institution had reached yet.
          toast.success(
            `${p.name} founded, and its application is with the scheme. It can open ` +
              `customer accounts now; it can pay and be paid once the central bank and ` +
              `the clearing house have answered. A listener of its own comes with the ` +
              `next server restart.`,
          );
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
              A participant is a bank in the network. This founds it — its own
              book, a customer subledger and a clearing suspense account — and
              applies to the scheme on its behalf. Joining is a conversation
              between three institutions, so it is a founded bank that comes
              back, and the central bank and the clearing house answer after
              this dialog has closed.
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
            <FieldLabel htmlFor="participant-bic" required>
              BIC
            </FieldLabel>
            <Input
              id="participant-bic"
              value={bic}
              placeholder="e.g. AURODEFFXXX"
              onChange={(e) => setBic(e.target.value.toUpperCase())}
            />
            <p className="text-xs text-muted-foreground">
              The bank&apos;s ISO 9362 business identifier code — what a
              counterparty addresses it by, and what the mesh routes on.
            </p>
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
              Founding opens a suspense and a reserve account per asset in the
              bank&apos;s own book, and asks the central bank for a settlement
              account in each. A bank can only hold money in an asset it joined
              with, and the set cannot be changed afterwards.
            </p>
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={
                add.isPending ||
                !name.trim() ||
                !bic.trim() ||
                assets.length === 0
              }
            >
              {add.isPending ? "Founding…" : "Found and apply"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
