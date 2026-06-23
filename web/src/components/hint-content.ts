// Typed registry of teaching hints, distilled from README.md (the authoritative
// source). Keep each entry to a few plain sentences — the "?" popover is a
// glance, not a chapter. Use <Hint id="..."> to render one; HintKey gives
// autocomplete and compile-time checking that the key exists.

export interface HintEntry {
  title: string;
  body: string;
}

export const hintContent = {
  "double-entry": {
    title: "Double-entry bookkeeping",
    body: "Every transaction has equal debits and credits, so money never appears or disappears — it only moves between accounts. If debits don't equal credits, something is wrong.",
  },
  "normal-balance": {
    title: "Normal balance",
    body: "Each account type has a side that increases it. Assets and Expenses increase with a debit; Liabilities, Equity and Revenue increase with a credit.",
  },
  "account-type-asset": {
    title: "Asset",
    body: "Things the bank owns or is owed: cash, reserves at the central bank, loans to customers, securities. Debit-normal — a debit increases it.",
  },
  "account-type-liability": {
    title: "Liability",
    body: "What the bank owes others. A customer's deposit is the bank's liability — it owes that money back on demand. Credit-normal.",
  },
  "account-type-equity": {
    title: "Equity",
    body: "The owners' residual interest: assets minus liabilities. Paid-in capital and retained earnings. Credit-normal.",
  },
  "account-type-revenue": {
    title: "Revenue",
    body: "Income the bank earns — interest on loans, fees. Credit-normal. Closed into equity at year-end.",
  },
  "account-type-expense": {
    title: "Expense",
    body: "Costs of running the bank — interest paid to depositors, salaries, loan-loss provisions. Debit-normal. Closed into equity at year-end.",
  },
  "ledger-vs-subledger": {
    title: "Ledger and subledger",
    body: "A ledger is the top-level book (e.g. the General Ledger). A subledger groups related accounts inside it — the GL shows 'Total Customer Deposits' while the subledger holds the 50,000 individual accounts that sum to it.",
  },
  "amount-cents": {
    title: "Amounts are integer cents",
    body: "All amounts are integers in the smallest currency unit (cents), like Stripe and most banks. This avoids floating-point rounding errors. €30.00 is stored as 3000.",
  },
  "idempotency-key": {
    title: "Idempotency key",
    body: "A unique key (UUID) per logical post. If the same key arrives twice — say a network retry — the backend rejects the duplicate instead of posting twice. One is generated for you automatically.",
  },
  reversal: {
    title: "Reversal",
    body: "Posted transactions are never edited or deleted. To correct one, a reversal is posted that flips every debit to a credit and vice-versa, netting to zero. The original is marked Reversed and the reversal references it.",
  },
  "booking-date": {
    title: "Booking date",
    body: "When the transaction was recorded in the system (the processing date). It drives audit trails and system reports. Defaults to now if you leave it blank.",
  },
  "value-date": {
    title: "Value date",
    body: "When the transaction takes economic effect — when interest starts and funds become available. It can be back- or forward-dated relative to the booking date (e.g. a Friday wire value-dated to Monday).",
  },
  "balance-book": {
    title: "Book balance",
    body: "The balance from all posted transactions, by booking date — everything recorded in the system, regardless of value date.",
  },
  "balance-holds": {
    title: "Holds",
    body: "The total of active authorization holds. Holds reduce the available balance without touching the book balance, and never appear in the ledger until captured.",
  },
  "balance-available": {
    title: "Available balance",
    body: "What an ATM or POS terminal can actually spend: Book balance − active holds + overdraft limit. Can differ sharply from the book balance.",
  },
  overdraft: {
    title: "Overdraft limit",
    body: "How far the available balance may go below zero. With a €500 limit, a €0 account can still process €500 of debits. Asset and Expense accounts hard-decline instead.",
  },
  holds: {
    title: "Holds (auth / capture)",
    body: "Models the card auth-capture flow: an authorization reserves funds (available balance drops, book balance unchanged). Capturing it posts a real transaction for the final amount; releasing it restores the balance with nothing hitting the ledger.",
  },
  "hold-capture": {
    title: "Capture a hold",
    body: "Turns the reserved amount into a real posted transaction — debiting the customer and crediting the counterparty. You can capture for the actual amount, up to the held amount.",
  },
  "hold-release": {
    title: "Release a hold",
    body: "Cancels the authorization. The available balance is restored and nothing is ever posted to the ledger — as if it never happened.",
  },
  "account-status": {
    title: "Account status",
    body: "Active = fully usable. Dormant = inactive, credits only (an incoming payment reactivates it). Frozen = view-only, set by legal/fraud action. Closed = terminal, requires a zero balance and can't be reopened.",
  },
  "scheme-direction-push": {
    title: "Push scheme",
    body: "The payer's bank initiates and sends the funds (e.g. SEPA Credit Transfer). No mandate needed. Money still flows debtor → creditor.",
  },
  "scheme-direction-pull": {
    title: "Pull scheme",
    body: "The payee's bank collects the funds under a pre-signed mandate (e.g. SEPA Direct Debit). Money still flows debtor → creditor — direction only governs who initiates.",
  },
  "settlement-model-net": {
    title: "Net settlement",
    body: "Payments are batched into a clearing cycle; only the net position between banks moves at settlement. This is how SEPA works today.",
  },
  "settlement-model-gross": {
    title: "Gross settlement",
    body: "Each payment settles individually and immediately, with no netting (e.g. instant payments, RTGS). Designed for but not yet wired up here.",
  },
  "requires-mandate": {
    title: "Requires a mandate",
    body: "Pull schemes need a mandate: the debtor's standing authorization for this specific creditor to collect, within an amount limit. Push schemes don't.",
  },
  "allows-return": {
    title: "Allows return",
    body: "Whether a settled payment can be unwound by an R-transaction (e.g. a disputed direct debit). The return posts compensating entries that move funds back debtor-ward.",
  },
  "settlement-delay": {
    title: "Settlement delay",
    body: "How long after initiation the payment settles, which sets the value date. SEPA Credit Transfer is T+1; SEPA Direct Debit is T+2.",
  },
  mandate: {
    title: "Mandate",
    body: "A debtor's signed authorization letting one specific creditor pull funds, up to a maximum amount. Initiation checks the mandate exists, is active, matches both parties, and stays within its limit.",
  },
  "payment-lifecycle": {
    title: "Payment lifecycle",
    body: "Initiated → Accepted → Cleared → Settled. On acceptance the debtor leg posts (funds leave into clearing suspense); on settlement reserves move at the central bank and the creditor leg pays the payee. Rejected reverses before clearing; Returned unwinds after settlement.",
  },
  "debtor-leg": {
    title: "Debtor leg",
    body: "The entry that moves money out of the payer's account into the bank's clearing suspense, posted at acceptance and value-dated to settlement.",
  },
  "creditor-leg": {
    title: "Creditor leg",
    body: "The entry that delivers funds into the payee's account, posted at settlement once reserves have moved at the central bank.",
  },
  "clearing-vs-settlement": {
    title: "Clearing vs. settlement",
    body: "Clearing is exchanging and netting instructions — agreeing who owes whom; no central-bank money moves. Settlement is the actual movement of reserves between banks at the central bank — the moment of finality.",
  },
  netting: {
    title: "Netting",
    body: "Customers move by gross amounts, but banks settle only the net. If A→B is 30000 and B→A is 10000, only 20000 of reserves moves. Net positions always sum to zero, so the settlement transaction balances.",
  },
  "net-positions": {
    title: "Net positions",
    body: "Per-bank net owed/owing for a clearing cycle. Negative = the bank pays in; positive = it receives. They sum to zero across all participants.",
  },
  "reserve-account": {
    title: "Reserve at central bank",
    body: "Each bank's claim on the central bank (an asset). It moves only at settlement and mirrors the bank's reserve liability in the central-bank ledger — the classic nostro/vostro reconciliation.",
  },
  "central-bank-reserves": {
    title: "Central-bank reserves",
    body: "The central bank holds one reserve liability per participant — what it owes each member bank. Banks meet only here; settlement is reserves moving between these accounts.",
  },
  "clearing-suspense": {
    title: "Clearing suspense",
    body: "A liability holding in-transit funds that have left a customer but not yet settled between banks. It returns to zero once the cycle settles.",
  },
  "audit-trail": {
    title: "Audit trail",
    body: "An immutable, append-only log of every mutation — each account creation, posting, hold, release, reversal and snapshot — with an ID, timestamp, type and affected entity. Nothing is ever deleted.",
  },
  snapshot: {
    title: "End-of-day snapshot",
    body: "A captured record of an account's three balances (book, holds, available) for a given day. Used for interest accrual, statements, regulatory reporting and faster balance queries.",
  },
} satisfies Record<string, HintEntry>;

export type HintKey = keyof typeof hintContent;
