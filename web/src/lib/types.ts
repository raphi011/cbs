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
  // Whether this line pools subsidiaries. A control account stands for many —
  // every customer's deposits, the whole loan book — and every posting against
  // it names whose. Two things depend on it: a posting form has to ask for the
  // subsidiary, and an account page shows the detail under the line rather than
  // only its total.
  control: boolean;
  createdAt: string;
}

export interface Entry {
  id?: string;
  accountId: string;
  // The subsidiary this leg belongs to within a control account — a deposit
  // account's id, a facility's. Absent means the whole account, which is what a
  // leg against one of the bank's own positions carries. A statement for one
  // customer is the legs against their control account WITH their id: without
  // it, every other customer's postings are in the same list.
  subsidiary?: string;
  amount: number;
  direction: Direction;
  // The entry's account's asset. Always populated on a response (never sent
  // by a client — a transaction request names accounts, not assets; see
  // EntryInput below).
  asset: string;
  // When THIS LEG takes economic effect, which is not always the
  // transaction's. A SEPA transfer's debtor posting value-dates the payer's
  // leg to the day of the debit and the suspense leg to settlement, days
  // apart — and the transaction-level `valueDate` is the settlement one, so
  // reading it for the payer's leg overstates when the customer's money
  // actually moved.
  //
  // Always populated on a response: the server resolves every leg's date
  // before storing it. Optional only because a leg written before the field
  // existed has none to report. See EntryInput for the input side.
  valueDate?: string;
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
  // The subsidiary these figures are for, absent when they are the whole
  // account's. On a control account the second is the sum of the first over
  // every subsidiary under it.
  subsidiary?: string;
  asset: string;
  balance: number;
  valueDateBalance: number;
}

// GET .../accounts/{aid}/subsidiaries: who a control account is holding money
// for, and how much of the line is each one's. Empty for a plain account, which
// pools nobody. What a subsidiary IS — a deposit account, a facility — the
// ledger does not know; the page that renders the link does.
export interface SubsidiaryBalance {
  subsidiary: string;
  asset: string;
  balance: number;
}

// --- Deposit layer --------------------------------------------------------

// DepositAccount's overdraft interest terms mirror a Facility's rate fields
// for the same reason: rate is millionths of rateScale (see web/src/lib/rate.ts),
// never a hardcoded 1_000_000. unarrangedRate applies to any balance drawn
// beyond overdraftLimit; accruedInterest is what the general ledger holds
// (rounded to a whole minor unit), not the sub-minor-unit precision the
// backend accrues at internally.
//
// A deposit account is not a line in the chart of accounts. Its money pools in
// `controlAccount` — one line per asset, for every customer of the bank — under
// this account's own `id`, and that pair is what a posting and a statement both
// name.
export interface DepositAccount {
  id: string;
  controlAccount: string;
  name: string;
  asset: string;
  status: DepositStatus;
  // The account's external addresses — what a counterparty quotes to pay it,
  // as against `id`, which is the bank's own key and never leaves it. Always
  // present, often empty: an account nobody pays from outside the bank needs no
  // address at all. See api/dto_deposit.go, which renders `[]` and not `null`.
  identifiers: AccountIdentifier[];
  // The catalogue entry pricing this account today. It varies over the
  // account's life — migrating between products is an ordinary forward-dated
  // row — so it is resolved as of today rather than fixed at opening.
  productId: string;
  overdraftLimit: number;
  overdraftRate: number;
  unarrangedRate: number;
  rateScale: number;
  dayCount: string;
  // Where overdraftRate came from: "product" is the product's list price and
  // "negotiated" is an overlay for this one customer. Without it a customer
  // cannot be told why their rate did not move when the product was repriced.
  pricingSource: PricingSource;
  accruedInterest: number;
  createdAt: string;
}

export type PricingSource = "product" | "negotiated";

// --- Product catalogue ----------------------------------------------------

// A catalogue entry: the named product an account is opened FROM.
//
// retired takes it off sale but does NOT unprice the accounts already on it,
// which keep resolving against its versions for as long as they live — so a
// form must filter retired entries out of what it offers, while an account
// page must still be able to name one.
export interface Product {
  id: string;
  name: string;
  kind: ProductKind;
  retired: boolean;
  createdAt: string;
}

export type ProductKind = "CurrentAccount";

// One row of a product's effective-dated timeline: what it cost from one day
// onwards, never changed once published.
//
// published false is a DRAFT — editable, and invisible to pricing, so the
// published row before it stays in force through its day. hash and publishedAt
// are stamped at publication and absent before it. There is no limit here, and
// the absence is the design: a limit is an underwriting decision about one
// customer, so it lives on the account's own terms timeline.
export interface ProductVersion {
  productId: string;
  effectiveFrom: string;
  rate: number;
  unarrangedRate: number;
  rateScale: number;
  dayCount: string;
  published: boolean;
  publishedAt?: string;
  hash: string;
  createdAt: string;
}

export interface CreateProductRequest {
  name: string;
  kind: ProductKind;
}

export interface DraftVersionRequest {
  effectiveFrom: string;
  rate: number;
  unarrangedRate: number;
  dayCount: string;
}

// One row of an account's effective-dated overdraft terms timeline. rate and
// unarrangedRate are millionths of rateScale, the same convention
// DepositAccount uses. effectiveFrom is when the row takes economic effect and
// createdAt is when it was entered — the two can differ in either direction, so
// a row whose effectiveFrom is in the future is on this list before it applies.
export interface OverdraftTerms {
  accountId: string;
  effectiveFrom: string;
  productId: string;
  overdraftLimit: number;
  // floating means the row carries no negotiated price, so its rate comes from
  // the product version in force on each day. The three rate fields are then
  // ZERO because the row holds nothing — which is not "interest-free", and
  // rendering them as a price would show every floating account as free.
  floating: boolean;
  rate: number;
  unarrangedRate: number;
  rateScale: number;
  dayCount: string;
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
  // Where a credit goes when the payee's account will not take it — closed, and
  // therefore terminal. The bank still owes the money, to whoever eventually
  // claims it, so it is a liability like a deposit.
  unclaimed: string;
  // The cash the bank is holding, and the only account here that is nobody
  // else's promise: reserve is a claim on the central bank, suspense is money
  // owed to a counterparty's customer, unclaimed is owed to somebody
  // unidentified. This is money.
  //
  // It is where cash paid in over the counter lands, so it is the contra leg of
  // every deposit — which is why it must be in this list rather than merely on
  // the wire. Moving it onto reserve is a LODGEMENT, a separate act.
  vaultCash: string;
  // This bank's account in the settlement agent's book — the only one here that
  // is not in the bank's own.
  //
  // It is also what says whether the scheme has answered this bank, and there is
  // no separate `status` field that says it again. Empty means no settlement
  // agent holds an account for it in this asset, which is an ordinary state: it
  // opens customer accounts, publishes products and takes cash deposits (POST
  // /deposits answers 200 and the cash lands in vaultCash), and it cannot LODGE
  // that cash, because only the central bank can credit an account in the
  // central bank's book — POST /lodgements is the 422. Nothing it takes part in
  // can settle either, a settlement instruction naming its members through a
  // routing directory this bank is not in. What still ROUTES to it is the mesh's
  // actor table, so a payment addressed to one clears like any other and the
  // cut-off carrying it is what fails.
  settlement: string;
}

export interface Participant {
  id: string;
  name: string;
  // The bank's ISO 9362 address: what a counterparty addresses it by and what
  // the mesh routes on. participantDTO has carried it all along; this interface
  // did not, because nothing in the UI needed it until the routing directory
  // became a screen — the roster is keyed by BIC and carries no name, so joining
  // the two is the only way to show a member's name beside its address.
  bic: string;
  // The bank's default deposit product, created with its chart of accounts at
  // onboarding. It is what the open-account form offers.
  productId: string;
  customerSubledger: string;
  assets: ParticipantAccounts[];
}

export interface AccountIdentifier {
  scheme: string;
  value: string;
}

// What GET /directory/accounts answers: which of the ASKING BANK's own accounts
// holds the address. `identifier` is echoed back so a client that fired several
// lookups at once can tell the answers apart. See api/handlers_directory.go's
// accountDirectoryEntryDTO.
//
// It carries no `name` and no `asset`, and it is not answerable network-wide.
// Either would be a join into whichever bank turns out to hold the address — one
// bank reading another's register for the payee's name, over HTTP. A bank holds
// its own register and no other, so the question it answers is "is this one of
// mine".
export interface DirectoryEntry {
  // The BIC of the bank the address resolves at, which is always the bank that
  // was asked: the lookup searches that bank's own register and no other, so
  // there is no other answer it could carry.
  agent: string;
  account: string;
  identifier: AccountIdentifier;
}

// One row of the copy of the scheme's routing directory that ONE BANK holds, as
// GET /directory/banks answers it. See api/handlers_directory.go's
// routingEntryDTO.
//
// A BIC and no name, which is the whole of what the roster it was copied from
// carries. A send form resolving an IBAN shows `AURODEFFXXX` and cannot show
// "Aurora Bank"; that is the documented absence arriving where a payer most
// expects a name, not a field this type trims.
//
// `refreshedAt` is on every row rather than beside the list because a snapshot
// replaces the copy wholesale, so every row of one pull carries one instant and
// any of them renders "refreshed 3 days ago". It is the only field here about
// the COPY rather than about the member, and it is what makes the subscription
// visible in a console.
export interface RoutingEntry {
  country: string;
  bankCode: string;
  bic: string;
  refreshedAt: string;
}

// One row of the clearing house's ROUTING directory: an address the scheme will
// send to, the allocation that member issues its customers' addresses under, the
// assets it clears in, the admission it was admitted under, and when. See
// api/handlers_directory.go's rosterEntryDTO.
//
// `country`/`bankCode` are what each member COPIES into its own RoutingEntry.
// The assets and the admission reference stay here: a copy of the assets would
// let a subscriber whose copy is behind refuse what the clearing house would
// accept, and the reference decides between two institutions contending for an
// address, which is nobody's question but this one's.
//
// No name. What writes this row delivers none, so the clearing house has never
// been told one — the name beside a BIC on this screen comes from GET /members,
// which is a different question asked of a different table.
export interface RosterEntry {
  bic: string;
  country: string;
  bankCode: string;
  assets: string[];
  admissionRef: string;
  admittedAt: string;
}

// One side of a payment or a mandate: the account, and the address quoted to
// reach it. It names NO BANK. A bank's id is its BIC, so a `participant` field
// beside the agent would be one value spelled twice; which bank a party is at
// travels as `debtorAgent`/`creditorAgent` on the enclosing payment or mandate,
// the same names an instruction uses on the way in. See api/dto_payment.go's
// partyRefDTO.
export interface PartyRef {
  account: string;
  // The external address quoted for this party — an IBAN today. Absent when
  // the party was addressed only by its ids.
  identifier?: AccountIdentifier;
}

export interface Payment {
  id: string;
  scheme: string;
  // The scheme's asset, resolved server-side (a payment names a scheme, not
  // an asset — see api/dto_payment.go's toPaymentDTO).
  asset: string;
  debtor: PartyRef;
  creditor: PartyRef;
  // The BICs of the two banks, and what says where each party banks now that a
  // PartyRef does not.
  debtorAgent?: string;
  creditorAgent?: string;
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

// What a bank answers a customer's instruction with: an identifier to ask about,
// not an outcome. See api/handlers_bank_payment.go's submittedPaymentDTO — named
// for submission rather than acceptance since 7b, because the payment this names
// is Initiated, in no cycle and unseen by the counterparty. That is also the
// reason it is 202 and not 201.
export interface AcceptedPayment {
  paymentId: string;
}

export interface Mandate {
  id: string;
  debtor: PartyRef;
  // The bank a collection under this mandate is sent to. There is no
  // creditorAgent beside it and no row to fill one from: a mandate is the
  // creditor's bank's, so the creditor's agent is whichever bank holds it.
  debtorAgent?: string;
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
  // Keyed by BIC: the settlement instruction turns net positions into addresses,
  // so a position is against a bank's address and not against a network id.
  netPositions?: Record<string, number>;
  openedAt: string;
  closedAt?: string;
  // There is no settlementId, and its absence says something true. That id is
  // allocated inside the SETTLEMENT AGENT's own unit of work in its own
  // database, and no message carries it back — what the clearing house is sent
  // is a pacs.002 quoting the CYCLE, because the cycle is what it asked about.
  // So `status` is what says a cut-off settled, and the settlement itself is on
  // the agent's own console, matched by cycleId. See api's clearingCycleDTO.
}

export interface Settlement {
  id: string;
  cycleId: string;
  // What was settled in, recorded on the row by the settlement agent from the
  // instruction it acted on. Resolving it server-side would follow the
  // settlement to its cycle to that cycle's scheme, which is a chain into
  // another institution's database. See payment.Settlement.Asset.
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
//
// The bank is named by its BIC, because the settlement agent's register is keyed
// by address and holds no participant ids — an id the network allocates is not
// something a message ever tells anybody.
export interface Reserve {
  agent: string;
  asset: string;
  reserve: number;
}

// --- Lending layer ----------------------------------------------------------

// Facility mirrors facilityDTO. A facility is not a line in the chart of
// accounts either: its money sits in the three control accounts below, under
// this facility's own `id`. `drawn` is DERIVED — the balance of
// `principalAccount` under it, not a stored field. `accruedInterest` is
// `Minor()` of the facility's own stored accrued figure — numerically equal to
// its share of `interestAccount` by the invariant the system maintains, but read
// from the record rather than the account; see api/dto_lending.go's
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
  principalAccount: string;
  interestAccount: string;
  // The line interest the bank owes this borrower back sits in. It exists from
  // the first facility in the asset; what is per borrower is the balance under
  // them. See `refundPayable`.
  refundAccount: string;
  commitment: number;
  drawn: number;
  accruedInterest: number;
  outstanding: number;
  // Interest the bank owes THIS borrower back, because a backdated posting
  // showed it charged interest that was never earned and the borrower had
  // already paid it in cash. Derived, like `drawn`: the balance of
  // `refundAccount` under this facility, and 0 in the ordinary case where no
  // correction has ever overshot.
  //
  // It is NOT part of `outstanding`, which is what the borrower owes the bank.
  // The money runs the other way, and netting it in would render a smaller loan
  // instead of an obligation.
  //
  // Discharging it is `POST /participants/{pid}/facilities/{fid}/
  // interest-refunds`, and every outstanding one across a bank is
  // `GET /participants/{pid}/interest-refunds-payable`. Neither is mirrored in
  // endpoints.ts, for the same reason disbursement, draws and repayments are
  // not: this client calls the routes the UI needs, and money moving out of the
  // bank is driven from the API rather than the browser.
  refundPayable: number;
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

export interface CreateAccountRequest {
  name: string;
  type: AccountType;
  // Required: the server refuses an account with no asset rather than
  // opening it in euro.
  asset: string;
}

// Entries on input carry accountId/amount/direction (no id, no asset — the
// asset a leg posts in is decided by the account it names).
export interface EntryInput {
  accountId: string;
  // Required when accountId is a control account and refused when it is not:
  // the ledger accepts no unqualified entry against a line that pools subsidiaries,
  // and no qualified one against a line that does not.
  subsidiary?: string;
  amount: number;
  direction: Direction;
  // Optional per-leg value date. OMITTED means "the transaction's", which is
  // the server's own rule for an unset one — so a client that does not care
  // about per-leg dates sends nothing rather than computing a date, and one
  // that does can pin a single leg without touching the others.
  valueDate?: string;
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
  // Required too: every deposit account is opened FROM a product, because a
  // floating terms row with no product would have nothing to float to.
  productId: string;
  overdraftLimit: number;
}

// The three requests that replaced SetOverdraftTermsRequest, one per decision
// the old single call conflated. effectiveFrom is RFC3339 and may point in
// either direction; absent means today on the server's clock.

export interface SetOverdraftLimitRequest {
  limit: number;
  effectiveFrom?: string | null;
}

// A null pricing CLEARS the overlay and puts the account back on its product,
// at whatever the product costs by then. That is not "interest-free": an
// interest-free account is a pricing with a zero rate.
export interface SetOverdraftPricingRequest {
  pricing: OverdraftPricing | null;
  effectiveFrom?: string | null;
}

export interface OverdraftPricing {
  rate: number;
  unarrangedRate: number;
  dayCount: string;
}

export interface ChangeProductRequest {
  productId: string;
  effectiveFrom?: string | null;
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
  // The subsidiary within the counterparty account, when it pools them —
  // another customer of this bank is a position, not an account of their own.
  // Omitted for one of the bank's own accounts.
  subsidiary?: string;
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

// TransferRequest is a book transfer between two customers of one bank.
//
// `from` is an account id and `to` is an IBAN, and the asymmetry is what a payer
// actually holds: you know which of your accounts the money is leaving, and about
// anybody else you know the address they gave you. It is the same pair a send
// form types for a payment out of the bank — what a payer says does not change
// because the money is not leaving.
//
// There is no scheme beside the address. Every account a bank opens is minted an
// IBAN, and a card number is a scheme somebody else issues rather than somewhere
// money is sent.
export interface TransferRequest {
  from: string;
  to: string;
  amount: number;
  description?: string;
}

// Transfer is the receipt: what the posting is called in the ledger, which
// account the address turned out to be, and what the PAYER has left.
//
// One balance and not two. The caller is the payer, and a transfer is not
// permission to read the payee's balance — that both accounts sit in one bank's
// register is a fact about the route, not a licence it grants. The payee's
// account id is here because the bank's own account directory already answers
// exactly that; their name is not.
export interface Transfer {
  transactionId: string;
  from: string;
  to: string;
  balance: Balance;
}

// LodgementRequest is a bank asking its central bank to move vault cash onto the
// bank's reserve account: the second half of funding one.
//
// It names an ASSET where FundRequest names an account, and the contrast is the
// whole difference. A deposit is about one customer's account, so the asset
// follows from it. A lodgement is about the BANK — its own cash, one pot per
// asset — and nothing else in the request says which. There is no default: a bank
// founded in dollars would have a euro lodgement invented for it by one.
export interface LodgementRequest {
  asset: string;
  amount: number;
}

// Lodgement is what POST /lodgements answers, and it is an INSTRUCTION rather
// than a balance.
//
// No reserve figure on it, because the reserve credit has not happened yet: it is
// the central bank's to make, on a camt.050 still in flight, and the route answers
// 202. A caller that reads a reserve straight afterwards may see the old number —
// the same asynchrony a submitted payment has.
//
// `ref` is the message identifier the camt.025 receipt quotes back. Nothing in the
// store is keyed by it, so it is for reading the log rather than for a follow-up
// request.
export interface Lodgement {
  ref: string;
  asset: string;
  amount: number;
  // account is the reserve account the credit was asked for, as this bank knows
  // it — the number it learned from its own admission acknowledgement.
  account: string;
  // agent is the central bank asked: the one party to the conversation the
  // request itself does not name.
  agent: string;
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

// It names the two accounts and the ceiling, and it names NO BANK. The address a
// collection is sent to is derived from the debtor's own IBAN through the
// creditor bank's copy of the routing directory, and it is derived ONCE: a
// mandate authorises debits from an account at the bank the debtor signed up
// against, so an authorisation that silently followed a directory to a different
// institution is a behaviour no real scheme has.
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
  // The names on the two accounts. Only the COUNTERPARTY's is required — the
  // creditor's on a push, the debtor's on a pull — because nothing looks it up:
  // the account is at another bank, so the name a payer types is the only one
  // there is. The submitting bank's own name is ignored, because a payer does
  // not rename themselves.
  //
  // THERE IS NO AGENT FIELD, and its absence is what makes this IBAN-only. The
  // BIC goes on the wire as CdtrAgt/DbtrAgt and the clearing house routes on it,
  // so a payer able to type one is a payer able to choose which bank receives
  // their money. It is derived instead, from the counterparty's own address
  // through the submitting bank's copy of the scheme's routing directory — see
  // api/dto_payment.go's initiatePaymentRequest and payment.SubmitPaymentTx.
  // Sending one is a 400: the backend rejects unknown fields.
  //
  // What that costs is a refusal a form has to be able to show: an address whose
  // bank code is not in this bank's copy is a 422, and the remedy is a refresh
  // or giving up, because a subscriber cannot tell "no such bank" from "my copy
  // is behind".
  debtorName?: string;
  creditorName?: string;
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
