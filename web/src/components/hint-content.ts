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
    body: `**Double-entry bookkeeping** is the rule that every transaction must have equal **debits** and **credits** — money never appears or disappears, it only moves between accounts.

Which side increases an account depends on its [[normal-balance]]. When Alice deposits €100: the bank's Cash asset is debited (+€100) and Alice's Deposit liability is credited (+€100). Both sides increase simultaneously.

\`\`\`
Customer deposits €100
  Debit  Cash (Asset)            100   ← bank now has more cash
  Credit Alice Deposit (Liability) 100  ← bank owes Alice more
                                  ───
  Net:                             0 ✓
\`\`\`

The balanced nature is a built-in error-detection mechanism: if debits ≠ credits the transaction is rejected outright. This invariant is also what lets a [[reversal]] cleanly undo any posting and what makes [[netting]] always sum to zero.`,
  },
  "normal-balance": {
    title: "Normal balance",
    body: `**Normal balance** is the side (debit or credit) that *increases* an account. Posting on the opposite side decreases it.

The five account types split into two groups by normal balance:

| Type | Normal balance | Increases with |
|------|---------------|----------------|
| Asset | Debit | Debit |
| Expense | Debit | Debit |
| Liability | Credit | Credit |
| Equity | Credit | Credit |
| Revenue | Credit | Credit |

So a debit to [[account-type-asset|an asset]] grows it, while a debit to [[account-type-liability|a liability]] shrinks it. This asymmetry is what keeps [[double-entry]] balanced: crediting Alice's Deposit liability (she has more) while debiting the bank's Cash asset (bank has more cash) are both *increases* on each type's normal side.`,
  },
  "account-type-asset": {
    title: "Asset",
    body: `An **asset** is anything of value the bank controls — things it owns or is owed. Its [[normal-balance]] is **debit**: a debit increases an asset, a credit decreases it.

Common bank assets: **Cash / central-bank reserves**, **loans to customers** (a mortgage is the bank's receivable), **securities** (bonds, investments), and **interbank lending**.

\`\`\`
Bank gives customer a €200,000 mortgage:
  Debit  Loans Receivable (Asset) 200000  ← new asset created
  Credit Cash (Asset)             200000  ← old asset consumed
\`\`\`

Notice both sides are assets — one converts into another. The bank doesn't lose €200,000; it exchanges one asset for another. See [[ledger-vs-subledger]] for how individual asset accounts nest under a General Ledger subledger.`,
  },
  "account-type-liability": {
    title: "Liability",
    body: `A **liability** is an obligation the bank owes to someone else. Its [[normal-balance]] is **credit**: a credit increases a liability, a debit decreases it.

The most important liability is **customer deposits** — the balance in your checking account is, from the bank's perspective, money it owes you on demand. This is often counterintuitive: the bank has the cash (asset), but it owes that cash back (liability).

\`\`\`
Customer deposits €500:
  Debit  Cash (Asset)             500  ← bank has more cash
  Credit Customer Deposit (Liab.) 500  ← bank owes customer more
\`\`\`

Other liabilities: borrowings from other banks, bonds payable, and the [[clearing-suspense]] account that holds in-transit payment funds.`,
  },
  "account-type-equity": {
    title: "Equity",
    body: `**Equity** is the owners' residual interest: **Assets − Liabilities**. If a bank has €100M in assets and €92M in liabilities, equity is €8M. Its [[normal-balance]] is **credit**.

Equity accounts change infrequently — mainly when shares are issued, bought back, or when year-end net income (Revenue − Expenses) is rolled into **Retained Earnings**.

\`\`\`
Accounting equation:
  Assets = Liabilities + Equity + (Revenue − Expenses)
\`\`\`

Profits increase equity (credit), losses decrease it (debit). Revenue and Expense accounts are **temporary** — they are "closed" into Retained Earnings at year-end, connecting the income statement to the balance sheet.`,
  },
  "account-type-revenue": {
    title: "Revenue",
    body: `**Revenue** tracks income flowing into the bank from its operations. Its [[normal-balance]] is **credit**: a credit increases revenue, a debit decreases it.

A bank's primary revenue source is **interest on loans** — it lends at a higher rate than it pays on deposits; the spread (net interest margin) is how banks make most of their money. Other sources: account fees, wire transfer fees, overdraft fees, and trading gains.

\`\`\`
Customer pays €50 monthly interest on a loan:
  Debit  Loan Receivable (Asset)   50  ← loan balance falls
  Credit Interest Income (Revenue) 50  ← bank earned income
\`\`\`

Revenue accounts are **temporary** — at year-end they are zeroed out and their net balance flows into Retained Earnings (equity).`,
  },
  "account-type-expense": {
    title: "Expense",
    body: `**Expense** accounts track the costs of running the bank. Their [[normal-balance]] is **debit**: a debit increases an expense, a credit decreases it.

The biggest bank expense is **interest paid to depositors** — the flip side of the net interest margin. Other expenses: salaries, rent, technology, provisions for loan losses, and compliance costs.

\`\`\`
Bank pays €10 monthly interest to a savings customer:
  Debit  Interest Expense (Expense) 10  ← cost recognised
  Credit Customer Deposit (Liab.)   10  ← bank owes customer more
\`\`\`

Like [[account-type-revenue|revenue]], expense accounts are **temporary** — closed into Retained Earnings at year-end.`,
  },
  "ledger-vs-subledger": {
    title: "Ledger and subledger",
    body: `A **ledger** is the top-level book of accounts — typically the **General Ledger (GL)** containing everything. A **subledger** is a subdivision of a ledger that groups related accounts: the GL shows one summary line while the subledger holds the individual detail.

\`\`\`
General Ledger
├── Customer Deposits (subledger)
│   ├── Alice Checking (Liability)
│   ├── Bob Checking  (Liability)
│   └── … 50,000 more accounts
├── Loans (subledger)
│   └── Loan #12345 (Asset)
└── Revenue (subledger)
    └── Fee Income (Revenue)
\`\`\`

The GL might show "Total Customer Deposits: €10M" while the Customer Deposits subledger contains 50,000 individual accounts that sum to that total. This lets regulators and management see the big picture in one row while operations can drill into any individual account. New accounts created via the API are always placed inside a subledger.`,
  },
  "amount-cents": {
    title: "Amounts are integer cents",
    body: `**All monetary amounts are stored as integers in the smallest currency unit** — cents for EUR/USD, pence for GBP. This is the same approach used by Stripe and most payment processors.

Floating-point arithmetic cannot represent most decimal fractions exactly: \`0.1 + 0.2 = 0.30000000000000004\` in IEEE 754. With integers there is no rounding error at all.

\`\`\`
Display amount → Internal storage
  €30.00        →  3000
  €1,234.56     →  123456
  €0.01         →  1
\`\`\`

The API always sends and receives integer cents. The frontend is responsible for converting to/from display format — that is what \`<MoneyInput>\` and the \`formatCents\` helper in \`money.ts\` do. Never pass a decimal like \`30.00\` to an endpoint.`,
  },
  "idempotency-key": {
    title: "Idempotency key",
    body: `An **idempotency key** is a unique identifier (UUID) attached to each logical posting request. If the same key arrives twice — say due to a network timeout and retry — the backend rejects the duplicate instead of posting the transaction twice.

In distributed systems, clients cannot always tell whether a request succeeded (the response may be lost in transit). Without idempotency, every retry risks creating duplicate transactions:

\`\`\`
Client sends POST /transactions  (key: "pay-001")
  → network timeout, no response received
Client retries POST /transactions (key: "pay-001")
  → server: "already processed pay-001" → returns error
  → client looks up the original transaction by key
\`\`\`

The key is generated for you automatically in the UI forms. If you need to re-drive a specific posting (e.g. from a test script), generate a fresh UUID each time — reusing a key is how you signal "this is the same logical operation", not "same amount".`,
  },
  reversal: {
    title: "Reversal",
    body: `A **reversal** corrects a posted transaction by creating a new transaction that exactly offsets it — every debit becomes a credit and vice versa. The ledger is **immutable**: posted transactions are never edited or deleted.

\`\`\`
Original (posted):
  Debit  Alice (Liability)  500
  Credit Bob   (Liability)  500

Reversal (new posting):
  Credit Alice (Liability)  500   ← flipped
  Debit  Bob   (Liability)  500   ← flipped
  ──────────────────────────────
  Net effect on both accounts: 0
\`\`\`

The original is marked **Reversed** for reporting; the reversal carries a reference back to it. This keeps the [[audit-trail]] intact — you can always see what happened and why. The [[idempotency-key]] of the reversal must be fresh (not the original's key). Reversals are also how a [[payment-lifecycle|rejected payment]] cleans up its [[debtor-leg]] before clearing.`,
  },
  "booking-date": {
    title: "Booking date",
    body: `The **booking date** is when the transaction was recorded in the system — the "processing date". It drives audit trails, system reports, and the order in which entries appear in the ledger.

Booking date and [[value-date]] can differ by days or even weeks in real-world scenarios. A wire transfer received Friday evening may be **booked immediately** (booking date: Friday 7 PM) but the funds become available only on Monday — that is the value date.

The booking date defaults to "now" if you leave the field blank. Back-dating or forward-dating the booking date itself is unusual; it is more common to leave booking date as the current time and adjust only the [[value-date]]. Interest accrual and regulatory balance reports use value date, not booking date.`,
  },
  "value-date": {
    title: "Value date",
    body: `The **value date** is when the transaction takes economic effect — when interest starts accruing and funds become available. It can be back- or forward-dated relative to the [[booking-date]].

Common cases where the two dates diverge:

- **Weekend processing:** wire booked Friday, value-dated Monday
- **Check deposits:** booked today, value-dated T+2 or T+3 while the check clears
- **Back-dated corrections:** operations books today, value-dates to the correct past date so interest calculations are right
- **Scheduled payments:** instruction booked now, value date is the 1st of next month

For [[payment-lifecycle|interbank payments]], the value date is determined by the [[settlement-delay]] of the scheme (T+1 for SEPA Credit Transfer, T+2 for SEPA Direct Debit). Interest accrual always uses value date — using the wrong date would cause customers to earn too much or too little interest.`,
  },
  "balance-book": {
    title: "Book balance",
    body: `The **book balance** (also called the ledger balance) is the balance computed from all **posted transactions**, ordered by booking date. It is the raw sum of every debit and credit ever recorded against the account.

\`\`\`
Available Balance = Book Balance − Active Holds + Overdraft Limit
\`\`\`

Book balance can differ from [[balance-available|available balance]] when there are active [[holds]]: a €100 authorization hold reduces what you can spend but doesn't change the book balance until the hold is [[hold-capture|captured]] as a real posting.

It can also differ from the value-date balance: a forward-dated transaction may be in the book balance before its economic effect begins. The [[snapshot|end-of-day snapshot]] records the book balance alongside holds and available balance for each business day.`,
  },
  "balance-holds": {
    title: "Holds",
    body: `**Holds** (also called pending authorizations) are the total reserved amount across all active authorization holds on an account. They reduce the [[balance-available|available balance]] without touching the [[balance-book|book balance]].

Holds never appear in the ledger — they are tracked by the deposit layer only. The book balance (and therefore the trial balance) is unaffected until a hold is [[hold-capture|captured]] and converted into a real posting.

\`\`\`
Book balance:      €1,000
Active holds:       −€200  (e.g. two card authorizations)
Available balance:  €800
\`\`\`

The total holds figure here is the aggregate of all currently active holds. Individual holds can be [[hold-capture|captured]] for the final amount (up to the reserved amount) or [[hold-release|released]] with no ledger impact.`,
  },
  "balance-available": {
    title: "Available balance",
    body: `The **available balance** is what can actually be spent right now — what ATMs and POS terminals check before approving a transaction.

\`\`\`
Available Balance = Book Balance − Active Holds + Overdraft Limit
\`\`\`

It can differ sharply from the [[balance-book|book balance]]. Example with a €500 [[overdraft]] limit:

\`\`\`
Book balance:      €200
Active holds:       −€150
Overdraft limit:   +€500
Available balance:  €550   ← still plenty of room
\`\`\`

[[account-type-asset|Asset]] and [[account-type-expense|expense]] accounts hard-decline debits when available balance would go below zero — they have no overdraft. Liability accounts (customer deposits) can have a configured overdraft limit that extends available balance below zero.`,
  },
  overdraft: {
    title: "Overdraft limit",
    body: `An **overdraft limit** is how far the [[balance-available|available balance]] may go below zero. It is a business rule enforced *before* posting — the ledger simply records the economic reality.

\`\`\`
Book balance:         €200
Overdraft limit:      €500
Max debit possible:   €700
After a €600 debit:
  Book balance:      −€400  (customer now owes the bank)
  Available left:     €100  (limit €500, used €400)
\`\`\`

When a liability account's book balance goes negative, the bank's perspective flips: it is now an asset (the customer owes the bank). Interest is typically charged on the overdrawn amount at a higher rate than standard lending.

[[account-type-asset|Asset]] and [[account-type-expense|expense]] accounts always **hard-decline** — they return \`ErrInsufficientBalance\` rather than going negative. Only deposit (liability) accounts support an overdraft limit.`,
  },
  holds: {
    title: "Holds (auth / capture)",
    body: `**Holds** model the card **authorization → capture** flow: an authorization reserves funds so the [[balance-available|available balance]] drops immediately, while the [[balance-book|book balance]] stays unchanged until the final amount is known.

The three-step lifecycle:

1. **Authorization:** customer taps card at a gas pump; bank places a €100 hold. Available balance drops €100, book balance unchanged.
2. **[[hold-capture|Capture]]:** customer pumps €45 of gas; the hold is captured for €45. A real posting hits the ledger; the remaining €55 is freed.
3. **[[hold-release|Release]]:** customer drives off without pumping; hold released. Available balance restored, nothing ever hits the ledger.

Holds have an expiration time — if not captured within that window they automatically stop affecting available balance. This is exactly why holds live one layer above the [[ledger-vs-subledger|general ledger]]: the ledger stays a pure record of settled value.`,
  },
  "hold-capture": {
    title: "Capture a hold",
    body: `**Capturing a hold** converts the reserved authorization into a real posted transaction. The hold is removed and a [[double-entry]] posting is made in the [[ledger-vs-subledger|general ledger]] for the **actual** amount (which may be less than the originally held amount).

\`\`\`
Before capture:
  Book balance:      €1,000
  Active hold:        −€100  (gas station auth)
  Available balance:  €900

After capture of €45:
  Book balance:       €955   ← real posting: −€45
  Active hold:          €0   ← hold removed
  Available balance:  €955   ← released €55 + booked −€45
\`\`\`

You can capture for any amount up to the held amount. The counterparty account is credited in the same posting. If you capture for less than the held amount, the remainder is released automatically — the available balance recovers the difference.`,
  },
  "hold-release": {
    title: "Release a hold",
    body: `**Releasing a hold** cancels the authorization entirely. The [[balance-available|available balance]] is fully restored and **nothing is ever posted** to the [[ledger-vs-subledger|general ledger]] — from an accounting perspective it never happened.

\`\`\`
Before release:
  Book balance:      €1,000
  Active hold:        −€100
  Available balance:  €900

After release:
  Book balance:      €1,000  ← unchanged
  Active hold:          €0   ← removed
  Available balance: €1,000  ← fully restored
\`\`\`

This is the correct path when an authorization was placed in error, the merchant cancelled the transaction, or the hold simply expired. Compare to [[hold-capture|capture]], which does post to the ledger. Once released, the hold ID is gone — it cannot be captured afterwards.`,
  },
  "account-status": {
    title: "Account status",
    body: `**Account status** governs which operations are permitted. There are four states in the deposit layer (the [[ledger-vs-subledger|general ledger]] itself has no concept of status):

| Status | Allowed operations |
|--------|-------------------|
| **Active** | All: debits, credits, holds, statements |
| **Dormant** | Credits only — an incoming payment reactivates it |
| **Frozen** | View balance only — set by legal/fraud action |
| **Closed** | None — terminal, requires zero balance |

Transitions: Active ↔ Dormant (inactivity timer / incoming credit), any state → Frozen (freeze action), Frozen → prior state (unfreeze), Active → Closed (zero balance, no pending holds). **Closed is terminal** — the account cannot be reopened.

A **Frozen** account blocks card authorizations ([[holds]]), debits, and credits. The freeze preserves the previous state so that unfreeze returns to Active or Dormant correctly.`,
  },
  "scheme-direction-push": {
    title: "Push scheme",
    body: `In a **push** scheme the **payer's bank initiates** and sends the funds to the payee's bank. No [[mandate]] is needed because the payer is voluntarily pushing money out.

**SEPA Credit Transfer (SCT)** is the canonical push scheme: Alice instructs Bank A to credit Bob at Bank B. Bank A validates, posts the [[debtor-leg]], and submits the instruction to the clearing cycle.

\`\`\`
Direction of flow:
  Alice (debtor) → Bank A → clearing → Bank B → Bob (creditor)
  Initiated by:  Alice / Bank A   (the payer side)
\`\`\`

Money always flows **debtor → creditor** regardless of who initiates. [[scheme-direction-pull|Pull schemes]] reverse who *triggers* the instruction, but the underlying posting choreography is identical. The scheme's \`Direction\` field only governs initiation and [[requires-mandate]] — not which way reserves move.`,
  },
  "scheme-direction-pull": {
    title: "Pull scheme",
    body: `In a **pull** scheme the **payee's bank initiates** the collection by presenting a payment instruction to the payer's bank. The payer must have previously signed a [[mandate]] authorising this specific creditor to collect.

**SEPA Direct Debit (SDD)** is the canonical pull scheme: a utility company's bank submits a collection request against Alice's account at Bank A. Bank A validates the mandate and posts the [[debtor-leg]] — funds leave Alice just as they would in a push.

\`\`\`
Direction of flow:
  Alice (debtor) → Bank A → clearing → Bank B → Utility (creditor)
  Initiated by:  Utility / Bank B   (the payee side)
\`\`\`

Money still flows **debtor → creditor** — "direction" only means who triggers the instruction. Because the creditor initiates, [[requires-mandate|a mandate is required]] and [[allows-return|returns are allowed]] so the debtor can dispute a collection.`,
  },
  "settlement-model-net": {
    title: "Net settlement",
    body: `In **net settlement** payments are batched into a **clearing cycle**; only each participant's net position moves at settlement — not every individual payment. This is how SEPA works today.

\`\`\`
Cycle payments:
  Alice→Bob: €300   Bob→Alice: €100

Net positions:
  Bank A: −€200  (pays in)
  Bank B: +€200  (receives)
  ─────────────
  Sum:     €0  ✓

Reserves that actually move: €200  (not €400)
\`\`\`

[[netting]] reduces the volume of central-bank reserve movements dramatically. After [[clearing-vs-settlement|settlement]], both banks' [[clearing-suspense]] accounts return to zero. Compare to [[settlement-model-gross|gross settlement]], where each payment settles individually.`,
  },
  "settlement-model-gross": {
    title: "Gross settlement",
    body: `In **gross settlement** (also called **RTGS — Real-Time Gross Settlement**) each payment settles **individually and immediately** with no [[netting]]. There is no batching, no clearing cycle cut-off, and no net-position calculation.

\`\`\`
Payment: Alice→Bob €300
  → Central bank moves €300 reserves instantly
  → Creditor leg posted immediately
  → No waiting for cycle cut-off
\`\`\`

Examples: SEPA Instant, FedNow, Faster Payments. Gross settlement offers **instant finality** but requires each bank to maintain sufficient reserves for every individual payment — unlike net settlement where offsetting payments cancel out.

This codebase has the \`Scheme\` interface wired for \`SettlementModel() == Gross\`, but the gross settlement path in the orchestrator is not yet implemented. Net schemes ([[settlement-model-net]]) are fully operational.`,
  },
  "requires-mandate": {
    title: "Requires a mandate",
    body: `**Requires a mandate** flags whether a payment scheme demands a pre-signed [[mandate]] before a collection can proceed.

[[scheme-direction-pull|Pull schemes]] (e.g. SEPA Direct Debit) require a mandate because the **payee initiates** the debit — the payer's bank needs proof the payer consented. The backend checks: mandate exists, is active, the creditor matches, the debtor matches, and the amount is within the limit. Any failure → payment rejected.

[[scheme-direction-push|Push schemes]] (e.g. SEPA Credit Transfer) do **not** require a mandate — the payer is voluntarily pushing money, so no standing authorisation is needed.

\`\`\`
SDD payment initiation check:
  1. Mandate exists?          ✓
  2. Mandate active?          ✓
  3. Creditor matches?        ✓
  4. Debtor matches?          ✓
  5. Amount ≤ mandate limit?  ✓  → proceed
  Any ✗ → ErrMandateRequired / ErrMandateRevoked / ErrMandateExceeded
\`\`\``,
  },
  "allows-return": {
    title: "Allows return",
    body: `**Allows return** flags whether a settled payment can be unwound by an **R-transaction** (return, recall, or refund). This is a scheme property — not all schemes permit it.

SEPA Direct Debit [[allows-return|allows returns]] because the debtor did not initiate the collection and may dispute it. A \`ReturnPayment\` call posts compensating entries that move funds back from creditor to debtor across the central bank, fully restoring both customers' balances.

\`\`\`
Return flow (mirrors the original in reverse):
  Creditor account debited      ← funds leave payee
  Clearing suspense at Bank B   ← back into suspense
  Central bank: B pays A        ← reserves reverse
  Clearing suspense at Bank A   ← arrives at payer bank
  Debtor account credited       ← funds back to payer
\`\`\`

SEPA Credit Transfer does **not** allow returns in this model — once a push payment has settled, recall is a separate (out-of-band) process. In this codebase, returns settle immediately rather than being batched into a later R-cycle (a deliberate simplification).`,
  },
  "settlement-delay": {
    title: "Settlement delay",
    body: `**Settlement delay** is how long after initiation a payment settles, and it directly sets the **[[value-date]]** of the [[debtor-leg]] and [[creditor-leg]] postings.

| Scheme | Settlement delay | Value date |
|--------|-----------------|------------|
| SEPA Credit Transfer | T+1 | next business day |
| SEPA Direct Debit | T+2 | two business days out |
| Instant payments (Gross) | ~0 | same day |

\`\`\`
Payment initiated today (T):
  Debtor leg value date   = T + settlement delay
  Creditor leg value date = T + settlement delay
\`\`\`

During the settlement window the payment is in a **pending** state — the booking exists but reserves haven't moved. The [[payment-lifecycle]] moves from Accepted → Cleared → Settled as the cycle progresses. The [[balance-available|available balance]] reflects this gap via the [[holds|hold]] mechanism for card-style flows, or directly via the posted debtor leg for credit-transfer flows.`,
  },
  mandate: {
    title: "Mandate",
    body: `A **mandate** is a debtor's signed authorisation letting one specific creditor collect funds from their account, up to a maximum amount. It is required by [[scheme-direction-pull|pull schemes]] like SEPA Direct Debit.

The mandate records: **creditor ID**, **debtor account ID**, **maximum amount**, and **status** (active / revoked). At payment initiation the backend validates all four:

\`\`\`
Mandate checks on SDD initiation:
  creditor_id   == payment.creditor?    ✓
  debtor_id     == payment.debtor?      ✓
  status        == active?              ✓
  amount        ≤ mandate.max_amount?   ✓
  → payment accepted
\`\`\`

A revoked mandate causes immediate rejection (\`ErrMandateRevoked\`). An exceeded limit causes \`ErrMandateExceeded\`. Once a payment is settled, the debtor can trigger a [[allows-return|return]] to dispute the collection. Mandates are a network-level resource in the API — they are created before any payment, independent of a specific cycle.`,
  },
  "payment-lifecycle": {
    title: "Payment lifecycle",
    body: `Every payment travels through an explicit state machine with clearly separated clearing and settlement phases.

\`\`\`
Initiated ──▶ Accepted ──▶ Cleared ──▶ Settled
                  │                       │
                  ▼                       ▼
              Rejected                Returned
\`\`\`

- **Initiated → Accepted:** scheme validates (funds, [[mandate]] if needed); [[debtor-leg]] posted — payer's money moves into [[clearing-suspense]], value-dated to settlement date.
- **Accepted → Cleared:** clearing cycle closes; [[netting|net positions]] computed across all payments in the cycle. No money moves yet.
- **Cleared → Settled:** reserves move at the [[central-bank-reserves|central bank]]; [[creditor-leg]] posted — payee receives funds.
- **Rejected:** before clearing; [[reversal]] of the debtor leg restores the payer's balance.
- **Returned:** after settlement; an R-transaction fully unwinds the flow (available on [[allows-return|return-enabled]] schemes only).

See [[clearing-vs-settlement]] for why clearing and settlement are distinct phases, and [[settlement-delay]] for how the value date is set.`,
  },
  "debtor-leg": {
    title: "Debtor leg",
    body: `The **debtor leg** is the ledger entry that moves money out of the payer's account. It is posted at **acceptance** (when the scheme validates the payment), value-dated to the settlement date.

\`\`\`
Bank A — debtor leg (Alice pays €300 to Bob):
  Debit  Alice Deposit (Liability)    300  ← Alice's balance falls
  Credit Clearing Suspense (Liability) 300  ← funds held in transit
\`\`\`

The funds sit in [[clearing-suspense]] until the cycle settles. During this window the payment is "in flight" — Alice's money has left her account but hasn't reached Bob yet. This is the [[clearing-vs-settlement|clearing phase]].

The debtor leg is reversed if the payment is [[payment-lifecycle|rejected]] before clearing. If the payment proceeds to settlement, the suspense is cleared by a balancing entry and reserves move at the [[central-bank-reserves|central bank]].`,
  },
  "creditor-leg": {
    title: "Creditor leg",
    body: `The **creditor leg** is the ledger entry that delivers funds into the payee's account. It is posted at **settlement**, once reserves have actually moved between banks at the [[central-bank-reserves|central bank]].

\`\`\`
Bank B — creditor leg (Bob receives €300 from Alice):
  Debit  Clearing Suspense (Liability) 300  ← suspense cleared
  Credit Bob Deposit (Liability)       300  ← Bob's balance rises
  Debit  Reserve at CB (Asset)         300  ← Bank B's reserve asset rises
  Credit Clearing Suspense (Liability) 300  ← and suspense nets to zero
\`\`\`

The creditor leg is the moment of **finality** for the payee — funds are now permanently in their account. Until the creditor leg posts, Bob can see the payment as pending (value-dated) but the funds are not yet available. See [[payment-lifecycle]] for the full state machine and [[debtor-leg]] for the payer's side.`,
  },
  "clearing-vs-settlement": {
    title: "Clearing vs. settlement",
    body: `**Clearing** and **settlement** are two distinct phases that are often conflated.

**Clearing** is the exchange and [[netting]] of payment instructions — banks agree on who owes whom. No central-bank money moves. The output is a set of **[[net-positions|net positions]]** per bank.

**Settlement** is the actual movement of **reserves** between banks at the [[central-bank-reserves|central bank]] — the moment of **finality**. Once settled, a transaction is irrevocable.

\`\`\`
Clearing phase:
  Banks exchange instructions → net positions computed
  Central-bank reserves:  unchanged

Settlement phase:
  Central bank debits Bank A reserves: −net
  Central bank credits Bank B reserves: +net
  Banks clear their [[clearing-suspense]] accounts
\`\`\`

The gap between the two phases is the **settlement window** — during it, counterparty risk exists. The [[payment-lifecycle]] reflects this: a payment moves Accepted → Cleared before it can reach Settled.`,
  },
  netting: {
    title: "Netting",
    body: `Customers move money by **gross** amounts, but banks settle only the **net**. [[clearing-vs-settlement|Clearing]] computes net positions so that offsetting payments cancel out and only the residual moves at settlement.

\`\`\`
One clearing cycle:
  Alice (Bank A) → Bob   (Bank B): 30000
  Bob   (Bank B) → Alice (Bank A): 10000

Net positions:
  Bank A: −30000 + 10000 = −20000  (pays in)
  Bank B: +30000 − 10000 = +20000  (receives)
  ──────────────────────────────────
  Sum:                         0  ✓

Reserves that move: 20000  (not 40000 gross)
\`\`\`

[[net-positions|Net positions]] always sum to zero across all participants, so the [[central-bank-reserves|central bank]] settlement transaction is perfectly balanced under [[double-entry]]. Netting dramatically reduces the volume of reserve movements — two payments become one.`,
  },
  "net-positions": {
    title: "Net positions",
    body: `**Net positions** are the per-bank totals owed or owing for a single [[clearing-vs-settlement|clearing]] cycle. They are the output of [[netting]] across all payments in the cycle.

\`\`\`
After netting a cycle with N payments:
  Bank A net: −20000  → Bank A pays in €200
  Bank B net: +15000  → Bank B receives €150
  Bank C net:  +5000  → Bank C receives €50
  ─────────────────────
  Sum:              0  ✓  (always)
\`\`\`

A **negative** position means the bank pays reserves to the central bank at settlement; a **positive** position means it receives. They must sum to zero across all participants — this is the mathematical consequence of [[double-entry]] applied across the network.

After settlement, each bank's [[reserve-account|reserve asset]] changes by exactly its net position, and each bank's [[clearing-suspense]] account returns to zero.`,
  },
  "reserve-account": {
    title: "Reserve at central bank",
    body: `The **reserve at central bank** is each commercial bank's **asset account** representing its claim on the [[central-bank-reserves|central bank]]. It moves only at settlement and mirrors the bank's reserve liability in the central-bank ledger — the classic **nostro/vostro** reconciliation.

\`\`\`
Bank A's chart of accounts:
  Reserve at CB (Asset) ←── moves at settlement

Central Bank's chart of accounts:
  Reserve: Bank A (Liability) ←── the other side
\`\`\`

These two accounts must always agree: when Bank A's reserve asset rises by €200, the central bank's Reserve: Bank A liability also rises by €200. If they diverge, it signals a bookkeeping error. This is the [[double-entry]] invariant applied across the two institutions.

The reserve account is the ultimate destination of all [[net-positions|net settlement]] flows — no payment is final until it is reflected here.`,
  },
  "central-bank-reserves": {
    title: "Central-bank reserves",
    body: `The **central bank** holds one **reserve liability** per participant — what it owes each member bank. This is the only place where commercial banks actually "meet" and where [[clearing-vs-settlement|settlement]] happens.

\`\`\`
Central-bank ledger:
  Reserve: Bank A (Liability)  ← CB owes Bank A its reserves
  Reserve: Bank B (Liability)  ← CB owes Bank B its reserves
  Settlement Assets (Asset)    ← balancing asset when funded
\`\`\`

At settlement, the central bank transfers reserves between these liability accounts:

\`\`\`
Debit  Reserve: Bank A (Liability) 20000  ← A's balance falls
Credit Reserve: Bank B (Liability) 20000  ← B's balance rises
\`\`\`

The central bank's own books stay balanced under [[double-entry]] — one liability falls as another rises. Commercial banks never write into each other's ledgers; they only interact via these central-bank accounts. The corresponding [[reserve-account|reserve asset]] on each bank's own books moves in lockstep.`,
  },
  "clearing-suspense": {
    title: "Clearing suspense",
    body: `The **clearing suspense** account is a **[[account-type-liability|liability]] holding in-transit funds** that have left a customer account but have not yet settled between banks. It returns to zero at the end of every settlement cycle.

\`\`\`
Timeline for a SEPA Credit Transfer:

1. Acceptance: Alice's funds move into suspense
   Debit  Alice Deposit (Liability)     300  ← she paid
   Credit Clearing Suspense (Liability) 300  ← in transit

2. Settlement: suspense cleared
   Debit  Clearing Suspense             300  ← transit ends
   Credit Reserve at CB (Asset)         300  ← reserve asset falls
\`\`\`

The suspense balance at any point equals the total value of in-flight payments that have been accepted but not yet settled. If a cycle fails to settle, the suspense remains non-zero — a signal that requires investigation. The [[audit-trail]] records every posting in/out of suspense for reconciliation.`,
  },
  "audit-trail": {
    title: "Audit trail",
    body: `The **audit trail** is an **immutable, append-only log** of every mutation in the system. Nothing is ever deleted. Every event carries a unique ID, timestamp, event type, and the full payload of the affected entity.

Events recorded: account creation, transaction posting, [[holds|hold]] creation, [[hold-release|hold release]], [[hold-capture|hold capture]], [[reversal]], and [[snapshot|end-of-day snapshot]].

\`\`\`
Example audit events (most recent first):
  [2024-03-01T14:32:01Z] transaction_posted  txn-abc123  Alice −€300
  [2024-03-01T14:32:00Z] hold_captured       hold-xyz789 Alice −€300
  [2024-03-01T09:00:00Z] snapshot_taken      acct-001    book=€1000
  [2024-03-01T08:55:00Z] hold_created        hold-xyz789 Alice −€300
\`\`\`

The trail enables: regulatory compliance (banks must maintain complete records), forensic investigation, incident response, and **independent balance verification** — you can replay all events to recompute any balance from scratch, cross-checking against [[snapshot|snapshots]].`,
  },
  snapshot: {
    title: "End-of-day snapshot",
    body: `An **end-of-day snapshot** captures an account's three balances — [[balance-book|book]], [[balance-holds|holds]], and [[balance-available|available]] — at the close of a business day. It is taken by the deposit layer, not the general ledger (which only computes book balance on demand).

Snapshots serve four purposes:

1. **Interest accrual** — daily interest is calculated on the end-of-day balance. For 4% APR on €10,000: \`€10,000 × 0.04 / 365 = €1.10/day\`.
2. **Statement generation** — monthly statements show end-of-day balances, opening, and closing figures.
3. **Regulatory reporting** — regulators require daily position data.
4. **Performance** — balance queries can start from the latest snapshot and replay only subsequent transactions, rather than replaying the full history.

\`\`\`
Snapshot for 2024-03-01:
  account:   acct-001
  book:      €10,000
  holds:     €200
  available: €9,800
\`\`\`

The [[audit-trail]] records every snapshot event for complete auditability.`,
  },
} satisfies Record<string, HintEntry>;

export type HintKey = keyof typeof hintContent;
