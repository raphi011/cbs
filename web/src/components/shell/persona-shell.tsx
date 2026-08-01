"use client";

import { useIdentity } from "@/lib/identity";
import { BankShell } from "./bank-shell";
import { CentralBankShell } from "./central-bank-shell";
import { ClearingHouseShell } from "./clearing-house-shell";
import { PlainShell } from "./plain-shell";

// Who you are decides which software you get. The customer's shell arrives with
// the customer's screens in Task 12; until then a customer URL has no page to
// render and falls through to the plain shell like the lobby does.
export function PersonaShell({ children }: { children: React.ReactNode }) {
  const identity = useIdentity();
  switch (identity?.persona) {
    case "central-bank":
      return <CentralBankShell>{children}</CentralBankShell>;
    case "clearing-house":
      return <ClearingHouseShell>{children}</ClearingHouseShell>;
    case "bank":
      return <BankShell pid={identity.pid}>{children}</BankShell>;
    default:
      return <PlainShell>{children}</PlainShell>;
  }
}
