// Exact mirror of api/dto.go — verbatim JSON field names. All monetary amounts
// are integer **minor units of an asset** (never floats or strings) — cents
// for EUR, satoshi for BTC. The asset a given amount is denominated in is
// named alongside it: an `asset` field carrying the asset's code (see `Asset`
// below for the full definition, fetched separately per participant from
// GET /participants/{pid}/assets). There is no default asset: converting an
// amount to a major-unit string requires knowing which asset's `scale` to use
// (see web/src/lib/money.ts). Request bodies must match these shapes exactly:
// the backend uses DisallowUnknownFields(), so never send keys it doesn't
// define.

import type {
  AccountType,
  AssetClass,
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

// Mirrors assetDTO from GET /participants/{pid}/assets. Assets are
// book-scoped — each participant registers its own (see
// ledger.Book.CreateAsset) — so always resolve an amount's scale from the
// asset registry of the participant whose book the amount lives in, never
// from a global table.
export interface Asset {
  code: string;
  name: string;
  scale: number;
  class: AssetClass;
}

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
  asset: string;
  createdAt: string;
}

export interface Entry {
  id?: string;
  accountId: string;
  amount: number;
  direction: Direction;
  // The entry's account's asset. Always populated on a response (never sent
  // by a client — a transaction request names accounts, not assets; see
  // EntryInput below).
  asset: string;
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

// AuditQuery narrows an audit listing. The log is append-only and unbounded, so
// the backend defaults `limit` to 100 and caps it at 1000; `before` is the
// exclusive upper cursor on `seq` and pages BACKWARDS, while each page still
// reads oldest-first.
//
// `seq` is a store-global total order, not a per-book or per-scope counter, so a
// cursor only means "the next page" when it is replayed against the same
// endpoint and the same type/entity filter that produced it.
export interface AuditQuery {
  limit?: number;
  before?: number;
  type?: string;
  entity?: string;
}

export interface AuditEvent {
  seq: number;
  id: string;
  scope: string;
  timestamp: string;
  type: string;
  entityId: string;
  payload?: unknown;
  metadata?: Record<string, string>;
}

// GET .../accounts/{aid}/balance returns the book balance as an integer in the
// minor units of the account's asset — cents for EUR, satoshi for BTC. The
// scale needed to render it comes from the asset, not from this response.
export interface BookBalance {
  accountId: string;
  balance: number;
}

// --- Deposit layer --------------------------------------------------------

export interface DepositAccount {
  id: string;
  glAccount: string;
  name: string;
  asset: string;
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

// A participant's internal accounts for one asset. A bank clearing both a euro
// and a dollar scheme holds two of these — each of those accounts is
// denominated in exactly one asset.
export interface ParticipantAccounts {
  asset: string;
  suspense: string;
  reserve: string;
  settlement: string;
}

export interface Participant {
  id: string;
  name: string;
  customerSubledger: string;
  assets: ParticipantAccounts[];
}

export interface PartyRef {
  participant: string;
  account: string;
  iban?: string;
}

export interface Payment {
  id: string;
  scheme: string;
  // The scheme's asset, resolved server-side (a payment names a scheme, not
  // an asset — see api/dto_payment.go's toPaymentDTO).
  asset: string;
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
  // Resolved server-side from the debtor's own deposit account (a mandate
  // names no scheme — see api/dto_payment.go's toMandateDTO).
  asset: string;
  maxAmount: number;
  status: MandateStatus;
  createdAt: string;
}

export interface ClearingCycle {
  id: string;
  scheme: string;
  // The scheme's asset, resolved server-side (see toClearingCycleDTO).
  asset: string;
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
  // Resolved server-side via the settlement's cycle's scheme (see
  // toSettlementDTO).
  asset: string;
  netPositions: Record<string, number>;
  settlementTx: string;
  valueDate: string;
  settledAt: string;
}

export interface Scheme {
  id: string;
  // The unit the scheme settles in (see payment.Scheme.Asset()).
  asset: string;
  direction: SchemeDirection;
  settlementModel: SettlementModel;
  requiresMandate: boolean;
  allowsReturn: boolean;
  settlementDelay: string;
}

// One bank's reserve at the central bank, in one asset. Reserves in different
// assets are different things and are never added together.
export interface Reserve {
  participant: string;
  asset: string;
  reserve: number;
}

// --- Request bodies (match Go request DTOs exactly) -----------------------

export interface NameRequest {
  name: string;
}

export interface CreateAccountRequest {
  name: string;
  type: AccountType;
  // Required: the server refuses an account with no asset rather than
  // opening it in euro.
  asset: string;
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
  // Required, like CreateAccountRequest.asset.
  asset: string;
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
