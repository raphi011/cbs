// Exact mirror of api/dto.go — verbatim JSON field names. All monetary amounts
// are integer **cents** (minor units), never floats or strings. Request bodies
// must match these shapes exactly: the backend uses DisallowUnknownFields(), so
// never send keys it doesn't define.

import type {
  AccountType,
  CycleStatus,
  DepositStatus,
  DepositStatusAction,
  Direction,
  HoldStatus,
  MandateStatus,
  PaymentStatus,
  SchemeDirection,
  SettlementModel,
  TransactionStatus,
} from "./enums";

// --- Ledger layer ---------------------------------------------------------

export interface Ledger {
  id: string;
  name: string;
  createdAt: string;
}

export interface Subledger {
  id: string;
  ledgerId: string;
  name: string;
  createdAt: string;
}

export interface Account {
  id: string;
  subledgerId: string;
  name: string;
  type: AccountType;
  createdAt: string;
}

export interface Entry {
  id?: string;
  accountId: string;
  amount: number;
  direction: Direction;
}

export interface Transaction {
  id: string;
  idempotencyKey?: string;
  entries: Entry[];
  bookingDate: string;
  valueDate: string;
  status: TransactionStatus;
  description?: string;
  metadata?: Record<string, string>;
  reversalOf?: string;
  createdAt: string;
}

export interface AuditEvent {
  id: string;
  timestamp: string;
  type: string;
  entityId: string;
}

// GET .../accounts/{aid}/balance returns the book balance (integer cents).
export interface BookBalance {
  accountId: string;
  balance: number;
}

// --- Deposit layer --------------------------------------------------------

export interface DepositAccount {
  id: string;
  glAccount: string;
  name: string;
  status: DepositStatus;
  overdraftLimit: number;
  createdAt: string;
}

export interface Hold {
  id: string;
  accountId: string;
  amount: number;
  expiresAt?: string;
  description?: string;
  status: HoldStatus;
  createdAt: string;
}

export interface Balance {
  book: number;
  holds: number;
  available: number;
}

export interface Snapshot {
  accountId: string;
  date: string;
  balance: Balance;
  takenAt: string;
}

// --- Payment layer --------------------------------------------------------

export interface Participant {
  id: string;
  name: string;
  customerSubledger: string;
  suspenseAccount: string;
  reserveAccount: string;
  settlementAccount: string;
}

export interface PartyRef {
  participant: string;
  account: string;
  iban?: string;
}

export interface Payment {
  id: string;
  scheme: string;
  debtor: PartyRef;
  creditor: PartyRef;
  amount: number;
  mandateId?: string;
  endToEndId?: string;
  status: PaymentStatus;
  rejectReason?: string;
  cycleId?: string;
  bookingDate: string;
  valueDate: string;
  description?: string;
  metadata?: Record<string, string>;
  debtorLegTx?: string;
  creditorLegTx?: string;
  createdAt: string;
}

export interface Mandate {
  id: string;
  debtor: PartyRef;
  creditor: PartyRef;
  maxAmount: number;
  status: MandateStatus;
  createdAt: string;
}

export interface ClearingCycle {
  id: string;
  scheme: string;
  status: CycleStatus;
  paymentIds: string[];
  netPositions?: Record<string, number>;
  openedAt: string;
  closedAt?: string;
  settlementId?: string;
}

export interface Settlement {
  id: string;
  cycleId: string;
  netPositions: Record<string, number>;
  settlementTx: string;
  valueDate: string;
  settledAt: string;
}

export interface Scheme {
  id: string;
  direction: SchemeDirection;
  settlementModel: SettlementModel;
  requiresMandate: boolean;
  allowsReturn: boolean;
  settlementDelay: string;
}

export interface Reserve {
  participant: string;
  reserve: number;
}

// --- Request bodies (match Go request DTOs exactly) -----------------------

export interface NameRequest {
  name: string;
}

export interface CreateAccountRequest {
  name: string;
  type: AccountType;
}

// Entries on input carry only accountId/amount/direction (no id).
export interface EntryInput {
  accountId: string;
  amount: number;
  direction: Direction;
}

export interface PostTransactionRequest {
  idempotencyKey: string;
  entries: EntryInput[];
  bookingDate?: string | null;
  valueDate?: string | null;
  description?: string;
  metadata?: Record<string, string> | null;
}

export interface OpenDepositAccountRequest {
  name: string;
  overdraftLimit: number;
}

export interface StatusRequest {
  action: DepositStatusAction;
}

export interface CreateHoldRequest {
  amount: number;
  expiresAt?: string | null;
  description?: string;
}

export interface CaptureHoldRequest {
  counterparty: string;
  amount: number;
  description?: string;
}

// date is a calendar day, "YYYY-MM-DD".
export interface SnapshotRequest {
  date: string;
}

export interface FundRequest {
  account: string;
  amount: number;
  description?: string;
}

export interface CreateMandateRequest {
  debtor: PartyRef;
  creditor: PartyRef;
  maxAmount: number;
}

export interface InitiatePaymentRequest {
  scheme: string;
  debtor: PartyRef;
  creditor: PartyRef;
  amount: number;
  mandateId?: string;
  endToEndId?: string;
  description?: string;
  metadata?: Record<string, string> | null;
}

export interface OpenCycleRequest {
  scheme: string;
}

export interface ReasonRequest {
  reason: string;
}

export interface DescriptionRequest {
  description: string;
}
