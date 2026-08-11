"use client";

import { Input } from "@/components/ui/input";
import { GLAccountPicker } from "@/components/pickers/gl-account-picker";
import { useAllAccounts } from "@/lib/api/hooks";

// A POSITION is what money moves against: an account, and — when that account
// pools subsidiaries — whose money it is. The two are picked together because they
// are refused apart: the ledger takes no unqualified entry against a control
// account, and no qualified one against a plain account.
//
// The subsidiary field appears only once the picked account says it must, so
// posting to the bank's own vault cash still looks like picking one thing.
export interface PositionValue {
  account: string;
  subsidiary: string;
}

export const emptyPosition: PositionValue = { account: "", subsidiary: "" };

// complete reports whether a position may be posted to: an account, plus a
// subsidiary exactly when the account pools them. Callers disable their submit on
// it rather than letting the server refuse a form that looked finished.
export function usePositionComplete(pid: string) {
  const { data } = useAllAccounts(pid);
  return (value: PositionValue) => {
    const account = data?.find((a) => a.id === value.account);
    if (!account) return false;
    return account.control ? value.subsidiary.trim() !== "" : true;
  };
}

export function PositionPicker({
  pid,
  value,
  onChange,
  id,
}: {
  pid: string;
  value: PositionValue;
  onChange: (value: PositionValue) => void;
  id?: string;
}) {
  const { data } = useAllAccounts(pid);
  const pools = data?.find((a) => a.id === value.account)?.control ?? false;

  return (
    <div className="space-y-2">
      <GLAccountPicker
        id={id}
        pid={pid}
        value={value.account}
        // The subsidiary belongs to the account it was named under, so changing
        // the account clears it rather than carrying one line's customer onto
        // another line.
        onChange={(account) => onChange({ account, subsidiary: "" })}
      />
      {pools && (
        <Input
          value={value.subsidiary}
          onChange={(e) => onChange({ ...value, subsidiary: e.target.value })}
          placeholder="Whose? (deposit account or facility id)"
        />
      )}
    </div>
  );
}
