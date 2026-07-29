// Exact mirror of api/dto.go — verbatim JSON field names. All monetary amounts
// are integer **minor units of an asset** (never floats or strings) — cents
// for EUR, satoshi for BTC. The asset a given amount is denominated in is
// named alongside it: an `asset` field carrying the asset's code (see `Asset`
// below for the full definition, fetched once from GET /assets). There is no
// default asset: converting an
// amount to a major-unit string requires knowing which asset's `scale` to use
// (see web/src/lib/money.ts). Request bodies must match these shapes exactly:
// the backend uses DisallowUnknownFields(), so never send keys it doesn't
// define.

import type {
  AccountType,
  ArrearsBucket,
  AssetClass,
  CycleStatus,
  DepositStatus,
  DepositStatusAction,
  Direction,
  FacilityKind,
  FacilityStatus,
  HoldStatus,
  MandateStatus,
  PaymentStatus,
  SchemeDirection,
  SettlementModel,
  TransactionStatus,
} from "./enums";

// Mirrors assetDTO from GET /assets. The known assets are defined in Go
// (ledger.LookupAsset) rather than stored, so the list is network-wide and
// read-only — the same shape as GET /schemes, and for the same reason. Resolve
// an amount's scale from this list; never assume one.
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

// GET .../accounts/{aid}/balance[?asOf=<RFC3339>]. `balance` is an integer in
// the minor units of the account's asset — cents for EUR, satoshi for BTC —
// and `asset` is the code whose scale renders it. The two travel together so
// displaying a balance is one request, not three.
//
// `valueDateBalance` is the same account as of the end of the requested day,
// counting only entries that have taken economic effect. It is what interest
// is computed from, and it differs from `balance` whenever a posting is
// value-dated away from its booking date. `asOf` defaults to now.
export interface BookBalance {
  accountId: string;
  asset: string;
  balance: number;
  valueDateBalance: number;
}

// --- Deposit layer --------------------------------------------------------

// DepositAccount's overdraft interest terms mirror a Facility's rate fields
// for the same reason: rate is millionths of rateScale (see web/src/lib/rate.ts),
// never a hardcoded 1_000_000. unarrangedRate applies to any balance drawn
// beyond overdraftLimit; accruedInterest is what the general ledger holds
// (rounded to a whole minor unit), not the sub-minor-unit precision the
// backend accrues at internally. interestGlAccount is empty until the first
// non-zero rate is set (see api/dto_deposit.go).
export interface DepositAccount {
  id: string;
  glAccount: string;
  name: string;
  asset: string;
  status: DepositStatus;
  overdraftLimit: number;
  overdraftRate: number;
  unarrangedRate: number;
  rateScale: number;
  dayCount: string;
  accruedInterest: number;
  interestGlAccount?: string;
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

// All three numbers are integers in `asset`'s minor units. The asset is on the
// response for the same reason it is on BookBalance: a number with no asset is
// not an amount, and a client that has to fetch the account to find out will
// render the digits first.
export interface Balance {
  asset: string;
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

// --- Lending layer ----------------------------------------------------------

// Facility mirrors facilityDTO. `drawn` is DERIVED — the principal GL
// account's book balance, not a stored field. `accruedInterest` is `Minor()`
// of the facility's own stored accrued figure — numerically equal to the
// interest GL account's balance by the invariant the system maintains, but
// read from the record rather than the account; see api/dto_lending.go's
// toFacilityDTO. `rate` is millionths of
// `rateScale` (render with web/src/lib/rate.ts's formatRate, never a
// hardcoded 1_000_000). `method` is only present for a TermLoan (a
// RevolvingLine has no amortization method); `minPayment` only for a
// RevolvingLine, and shares `rateScale` rather than carrying its own scale
// field — see rate.ts's header comment.
export interface Facility {
  id: string;
  kind: FacilityKind;
  name: string;
  asset: string;
  principalGlAccount: string;
  interestGlAccount: string;
  commitment: number;
  drawn: number;
  accruedInterest: number;
  outstanding: number;
  rate: number;
  rateScale: number;
  dayCount: string;
  method?: string;
  termMonths?: number;
  minPayment?: number;
  daysPastDue: number;
  arrearsBucket: ArrearsBucket;
  nonPerforming: boolean;
  status: FacilityStatus;
  openedAt: string;
  maturityAt?: string;
}

// Installment mirrors installmentDTO — one row of a facility's amortization
// schedule, as it was planned at disbursement (or, for a RevolvingLine,
// appended one per billing cycle). `outstanding` is THIS instalment's own
// unpaid remainder — (principal - paidPrincipal) + (interest - paidInterest)
// — not the facility's overall remaining balance; see
// lending.Installment.Outstanding.
export interface Installment {
  seq: number;
  dueDate: string;
  principal: number;
  interest: number;
  paidPrincipal: number;
  paidInterest: number;
  outstanding: number;
}

// Charge mirrors chargeDTO from POST
// /participants/{pid}/facilities/{fid}/interest-charge. Both halves are
// optional and INDEPENDENT: a cycle whose accrued interest has not yet reached
// a whole minor unit bills an instalment with no posting behind it, so a
// caller must not read an absent `transaction` as "nothing happened". The
// third case — nothing accrued and nothing drawn — is a 204 with no body at
// all, which is why chargeFacilityInterest's return type includes `undefined`.
export interface Charge {
  transaction?: Transaction;
  installment?: Installment;
}

// Totals mirrors totalsDTO from GET /participants/{pid}/totals. `overdrafts`
// is DERIVED — the sum of negative deposit-account balances by sign, not a
// figure any posting produces — see deposit.Totals and
// api/dto_lending.go's toTotalsDTOs.
export interface Totals {
  asset: string;
  deposits: number;
  overdrafts: number;
}

// --- Request bodies (match Go request DTOs exactly) -----------------------

export interface NameRequest {
  name: string;
}

// POST /participants. `assets` is the set of assets the bank joins with — one
// suspense, reserve and settlement account is provisioned per entry, and only
// those assets can hold money at this bank afterwards. Omitting it (or sending
// an empty array) means ["EUR"]; that is a default for the joining *set*, not
// for the asset of any individual account.
export interface AddParticipantRequest {
  name: string;
  assets?: string[];
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

// openFacilityRequest opens either product behind one route: `kind` selects
// which. `method`/`termMonths` apply only to a TermLoan; `minPayment` only to
// a RevolvingLine — see api/dto_lending.go's openFacilityRequest.
export interface OpenFacilityRequest {
  kind: FacilityKind;
  name: string;
  asset: string;
  commitment: number;
  rate: number;
  dayCount: string;
  method?: string;
  termMonths?: number;
  minPayment?: number;
}

// date is a calendar day, "YYYY-MM-DD", like SnapshotRequest.
export interface ChargeFacilityInterestRequest {
  date: string;
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
