"use client";

import { Input } from "@/components/ui/input";
import { FieldLabel } from "@/components/field-label";
import { ParticipantPicker } from "@/components/pickers/participant-picker";
import { DepositAccountPicker } from "@/components/pickers/deposit-account-picker";
import type { PartyRef } from "@/lib/types";

// What a FORM holds about one side of a payment or mandate. `ref` is the party
// as it goes out — an account and an optional IBAN, naming no bank. `pid` never
// leaves this console: it is the handle the bank was picked by, kept only
// because the account picker needs a register to search.
//
// There is no `agent`. The BIC is not a field on either request any more — it is
// derived from the party's own address, through the submitting bank's copy of
// the scheme's routing directory — so a draft holding one would be a value this
// form could not send anywhere.
export interface PartyDraft {
  pid: string;
  ref: PartyRef;
}

export const emptyParty: PartyDraft = { pid: "", ref: { account: "" } };

// A party in a payment or mandate: the bank plus the customer's deposit account
// within it, and an IBAN. Picking a bank scopes the account picker to it;
// changing the bank clears the account so the two can't disagree.
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
  // Whether the enclosing request needs an address for this side. The reason
  // differs by side and by console, so the decision is the caller's — see
  // InitiatePaymentForm, where both sides need one and for two different
  // reasons.
  addressRequired?: boolean;
}) {
  const idBase = legend.toLowerCase();
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
            onChange={(pid) => onChange({ pid, ref: { account: "" } })}
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
