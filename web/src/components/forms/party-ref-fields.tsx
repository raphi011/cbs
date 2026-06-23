"use client";

import { Input } from "@/components/ui/input";
import { FieldLabel } from "@/components/field-label";
import { ParticipantPicker } from "@/components/pickers/participant-picker";
import { DepositAccountPicker } from "@/components/pickers/deposit-account-picker";
import type { PartyRef } from "@/lib/types";

// A party in a payment or mandate: the bank (participant) plus the customer's
// deposit account within it, and an optional IBAN. Picking a participant scopes
// the account picker to that participant; changing the participant clears the
// account so the two can't disagree.
export function PartyRefFields({
  legend,
  value,
  onChange,
}: {
  legend: string;
  value: PartyRef;
  onChange: (next: PartyRef) => void;
}) {
  const idBase = legend.toLowerCase();
  return (
    <fieldset className="space-y-3 rounded-md border p-3">
      <legend className="px-1 text-sm font-medium">{legend}</legend>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <FieldLabel htmlFor={`${idBase}-participant`} required>
            Participant
          </FieldLabel>
          <ParticipantPicker
            id={`${idBase}-participant`}
            value={value.participant}
            onChange={(participant) =>
              onChange({ ...value, participant, account: "" })
            }
          />
        </div>
        <div className="space-y-1.5">
          <FieldLabel htmlFor={`${idBase}-account`} required>
            Deposit account
          </FieldLabel>
          <DepositAccountPicker
            id={`${idBase}-account`}
            pid={value.participant}
            value={value.account}
            onChange={(account) => onChange({ ...value, account })}
          />
        </div>
      </div>
      <div className="space-y-1.5">
        <FieldLabel htmlFor={`${idBase}-iban`}>IBAN (optional)</FieldLabel>
        <Input
          id={`${idBase}-iban`}
          value={value.iban ?? ""}
          placeholder="DE89…"
          className="font-mono"
          onChange={(e) =>
            onChange({ ...value, iban: e.target.value || undefined })
          }
        />
      </div>
    </fieldset>
  );
}

export const emptyPartyRef: PartyRef = { participant: "", account: "" };
