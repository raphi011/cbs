"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { ChevronsUpDown } from "lucide-react";

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Button } from "@/components/ui/button";
import { EnumBadge } from "@/components/enum-badge";
import { useIdentityDirectory } from "@/lib/api/hooks";
import { homeFor, useIdentity, type Identity } from "@/lib/identity";

// The switcher: one flat searchable list of complete identities, grouped
// Institutions / Banks / Customers, with customers under their bank. Selecting
// one navigates to its home.
//
// One control rather than a persona toggle plus a context picker, because a
// persona without its context is not an identity — "customer" alone addresses
// nothing, and two controls have a state where the persona has changed and the
// context has not.
//
// Frozen and Closed accounts are listed and selectable: seeing the customer view
// of a frozen account is one of the better lessons available here. A bank with
// no listener is not — it is shown as awaiting provisioning and cannot be
// chosen, because entering it would mean a console whose every request 502s.
//
// This control is out-of-fiction scaffolding, and deliberately so. Every
// persona's page reaches only its own operator — a retail client has no
// clearing-house connection (see endpoints.ts, identity.ts,
// handlers_bank_payment.go) — but useIdentityDirectory, which this picker
// calls, fires GET /members at the central bank and a deposit-accounts
// list at every bank, from inside the customer's own shell. That is not the
// persona boundary leaking; it is the seat-switcher, which by its nature has
// to see the whole cast to offer it, sitting outside the fiction it lets you
// step into. The honest alternative — a customer who can never see who else
// exists — would make the app harder to explore for no teaching gain, so the
// scaffolding stays and is named here rather than left to look like a bug.
export function IdentityPicker() {
  const [open, setOpen] = useState(false);
  const router = useRouter();
  const identity = useIdentity();
  const { banks, isLoading } = useIdentityDirectory();

  const bankName = (pid: string) =>
    banks.find((b) => b.participant.id === pid)?.participant.name ?? pid;

  const currentLabel = (() => {
    if (!identity) return "Choose an identity";
    if (identity.persona === "central-bank") return "Central bank";
    if (identity.persona === "clearing-house") return "Clearing house";
    if (identity.persona === "bank") return bankName(identity.pid);
    const account = banks
      .find((b) => b.participant.id === identity.pid)
      ?.accounts.find((a) => a.id === identity.did);
    return account ? `${account.name} · ${bankName(identity.pid)}` : "Customer";
  })();

  function go(next: Identity) {
    setOpen(false);
    router.push(homeFor(next));
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          role="combobox"
          aria-expanded={open}
          className="w-[240px] justify-between"
        >
          <span className="truncate">{currentLabel}</span>
          <ChevronsUpDown className="size-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[320px] p-0" align="end">
        <Command>
          <CommandInput placeholder="Search identities…" />
          <CommandList>
            <CommandEmpty>{isLoading ? "Loading…" : "No identity found."}</CommandEmpty>

            <CommandGroup heading="Institutions">
              <CommandItem
                value="central bank reserves settlement"
                onSelect={() => go({ persona: "central-bank" })}
              >
                Central bank
              </CommandItem>
              <CommandItem
                value="clearing house csm payments cycles schemes directory"
                onSelect={() => go({ persona: "clearing-house" })}
              >
                Clearing house
              </CommandItem>
            </CommandGroup>

            <CommandGroup heading="Banks">
              {banks.map(({ participant, provisioned }) => (
                <CommandItem
                  key={participant.id}
                  value={`bank ${participant.name} ${participant.id}`}
                  disabled={!provisioned}
                  onSelect={() =>
                    provisioned && go({ persona: "bank", pid: participant.id })
                  }
                >
                  <span className="truncate">{participant.name}</span>
                  {!provisioned && (
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      awaiting provisioning
                    </span>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>

            {banks
              .filter((b) => b.provisioned)
              .map(({ participant, accounts }) => (
                <CommandGroup key={participant.id} heading={`Customers · ${participant.name}`}>
                  {accounts.map((account) => (
                    <CommandItem
                      key={account.id}
                      value={`customer ${account.name} ${participant.name} ${account.identifiers
                        .map((i) => i.value)
                        .join(" ")}`}
                      onSelect={() =>
                        go({ persona: "customer", pid: participant.id, did: account.id })
                      }
                    >
                      <span className="truncate">{account.name}</span>
                      <EnumBadge value={account.status} />
                    </CommandItem>
                  ))}
                </CommandGroup>
              ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
