"use client";

import { Input } from "@/components/ui/input";
import { FieldLabel } from "@/components/field-label";
import { ParticipantPicker } from "@/components/pickers/participant-picker";
import { DepositAccountPicker } from "@/components/pickers/deposit-account-picker";
import { useParticipants } from "@/lib/api/hooks";
import type { PartyRef } from "@/lib/types";

// What a FORM holds about one side of a payment or mandate, which is more than
// the wire carries. `ref` is the party as it goes out — an account and an
// optional IBAN, naming no bank. `agent` is the BIC, which travels beside the
// ref on the enclosing request. `pid` is neither: it is the handle this console
// picked the bank by, kept because the account picker needs a register to search
// and a BIC is not what that route is keyed on.
export interface PartyDraft {
  pid: string;
  agent: string;
  ref: PartyRef;
}

export const emptyParty: PartyDraft = { pid: "", agent: "", ref: { account: "" } };

// A party in a payment or mandate: the bank plus the customer's deposit account
// within it, and an optional IBAN. Picking a bank scopes the account picker to
// it and fixes the agent BIC; changing the bank clears the account so the two
// can't disagree.
export function PartyRefFields({
  legend,
  value,
  onChange,
  name,
  onNameChange,
  addressRequired = false,
}: {
  legend: string;
  value: PartyDraft;
  onChange: (next: PartyDraft) => void;
  // The name on the account, rendered only where the enclosing request carries
  // one. A payment asserts it — the counterparty's is all a submitting bank
  // knows about the other side — and a mandate records no name at all.
  name?: string;
  onNameChange?: (next: string) => void;
  // Whether the scheme addresses this side by IBAN. It does for a payment's
  // COUNTERPARTY: the message quotes CdtrAcct/DbtrAcct and the submitting bank
  // has no register to resolve the other side's account in, so an unaddressed
  // party is refused. The submitting side is resolved from its own bank's
  // register and needs none.
  addressRequired?: boolean;
}) {
  const idBase = legend.toLowerCase();
  const { data: participants } = useParticipants();
  return (
    <fieldset className="space-y-3 rounded-md border p-3">
      <legend className="px-1 text-sm font-medium">{legend}</legend>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <FieldLabel htmlFor={`${idBase}-participant`} required>
            Bank
          </FieldLabel>
          <ParticipantPicker
            id={`${idBase}-participant`}
            value={value.pid}
            onChange={(pid) =>
              onChange({
                pid,
                agent: participants?.find((p) => p.id === pid)?.bic ?? "",
                ref: { account: "" },
              })
            }
          />
        </div>
        <div className="space-y-1.5">
          <FieldLabel htmlFor={`${idBase}-account`} required>
            Deposit account
          </FieldLabel>
          <DepositAccountPicker
            id={`${idBase}-account`}
            pid={value.pid}
            value={value.ref.account}
            onChange={(account) =>
              onChange({ ...value, ref: { ...value.ref, account } })
            }
          />
        </div>
      </div>
      {onNameChange && (
        <div className="space-y-1.5">
          <FieldLabel htmlFor={`${idBase}-name`} hint="counterparty-details">
            Name on the account
          </FieldLabel>
          <Input
            id={`${idBase}-name`}
            value={name ?? ""}
            onChange={(e) => onNameChange(e.target.value)}
          />
        </div>
      )}
      <div className="space-y-1.5">
        <FieldLabel htmlFor={`${idBase}-iban`} required={addressRequired}>
          {addressRequired ? "IBAN" : "IBAN (optional)"}
        </FieldLabel>
        <Input
          id={`${idBase}-iban`}
          value={value.ref.identifier?.value ?? ""}
          placeholder="DE89…"
          className="font-mono"
          onChange={(e) =>
            onChange({
              ...value,
              ref: {
                ...value.ref,
                identifier: e.target.value
                  ? { scheme: "IBAN", value: e.target.value }
                  : undefined,
              },
            })
          }
        />
      </div>
    </fieldset>
  );
}
