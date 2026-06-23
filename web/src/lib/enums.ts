// String unions mirroring the Go enum String() values, plus human labels and a
// color hint for badges. The Go side renders enums via String(), so these are
// the exact wire values.

export type AccountType =
  | "Asset"
  | "Liability"
  | "Equity"
  | "Revenue"
  | "Expense";

export type Direction = "Debit" | "Credit";

export type TransactionStatus = "Posted" | "Reversed";

export type DepositStatus = "Active" | "Dormant" | "Frozen" | "Closed";

// The four deposit status transitions, sent as {action} to POST .../status.
export type DepositStatusAction =
  | "freeze"
  | "unfreeze"
  | "markDormant"
  | "reactivate";

export type HoldStatus = "Active" | "Released" | "Captured";

export type PaymentStatus =
  | "Initiated"
  | "Accepted"
  | "Cleared"
  | "Settled"
  | "Rejected"
  | "Returned";

export type MandateStatus = "Active" | "Revoked";

export type CycleStatus = "Open" | "Closed" | "Settled";

export type SchemeDirection = "Push" | "Pull";

export type SettlementModel = "Net" | "Gross";

export const ACCOUNT_TYPES: AccountType[] = [
  "Asset",
  "Liability",
  "Equity",
  "Revenue",
  "Expense",
];

export const DIRECTIONS: Direction[] = ["Debit", "Credit"];

export const DEPOSIT_STATUS_ACTIONS: {
  action: DepositStatusAction;
  label: string;
}[] = [
  { action: "freeze", label: "Freeze" },
  { action: "unfreeze", label: "Unfreeze" },
  { action: "markDormant", label: "Mark dormant" },
  { action: "reactivate", label: "Reactivate" },
];

// The direction that increases each account type (its "normal balance").
export const NORMAL_BALANCE: Record<AccountType, Direction> = {
  Asset: "Debit",
  Expense: "Debit",
  Liability: "Credit",
  Equity: "Credit",
  Revenue: "Credit",
};

// Tone drives the <EnumBadge> color. "neutral" | "good" | "warn" | "bad" | "info".
export type Tone = "neutral" | "good" | "warn" | "bad" | "info";

export const STATUS_TONE: Record<string, Tone> = {
  // transaction
  Posted: "good",
  Reversed: "warn",
  // deposit account
  Active: "good",
  Dormant: "warn",
  Frozen: "info",
  Closed: "neutral",
  // hold
  Released: "neutral",
  Captured: "good",
  // payment
  Initiated: "info",
  Accepted: "info",
  Cleared: "info",
  Settled: "good",
  Rejected: "bad",
  Returned: "warn",
  // mandate
  Revoked: "bad",
  // cycle
  Open: "info",
};

// Account-type tone for the chart-of-accounts badges.
export const ACCOUNT_TYPE_TONE: Record<AccountType, Tone> = {
  Asset: "info",
  Liability: "warn",
  Equity: "good",
  Revenue: "good",
  Expense: "bad",
};
