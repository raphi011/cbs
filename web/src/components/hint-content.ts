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
  "minor-units": {
    title: "Amounts are integer minor units",
    body: `**All monetary amounts are stored as integers in the asset's smallest unit** — cents for EUR/USD, pence for GBP, satoshi for BTC. This is the same approach used by Stripe and most payment processors, extended here to any asset, not just fiat currency.

Floating-point arithmetic cannot represent most decimal fractions exactly: \`0.1 + 0.2 = 0.30000000000000004\` in IEEE 754. With integers there is no rounding error at all.

How many decimal places an asset's minor unit has is its **scale** (2 for EUR, 8 for BTC):

\`\`\`
Display amount → Internal storage (scale)
  €30.00        →  3000        (EUR, scale 2)
  €1,234.56     →  123456      (EUR, scale 2)
  ₿1.00000000   →  100000000   (BTC, scale 8)
\`\`\`

The API always sends and receives an integer at the asset's scale. The frontend is responsible for converting to/from display format — that is what \`<MoneyInput>\` and the \`formatAmount\` helper in \`money.ts\` do, given the asset (and its scale) the amount belongs to. Never pass a decimal like \`30.00\` to an endpoint.`,
  },
  "idempotency-key": {
    title: "Idempotency key",
    body: `An **idempotency key** is a unique identifier (UUID) attached to each logical posting request. If the same key arrives twice — say due to a network timeout and retry — the backend rejects the duplicate instead of posting the transaction twice.

(Design note: the industry-standard idempotency contract *replays the original response* on a repeat key, making the retry transparent. This codebase instead **rejects** the duplicate with an error and exposes a separate lookup-by-key — simpler, and enough to teach the invariant.)

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

The booking date defaults to "now" if you leave the field blank. Back-dating or forward-dating the booking date itself is unusual; it is more common to leave booking date as the current time and adjust only the [[value-date]]. Interest accrual uses value date, not booking date.`,
  },
  "value-date": {
    title: "Value date",
    body: `The **value date** is when the transaction takes economic effect — when interest starts accruing and funds become available. It can be back- or forward-dated relative to the [[booking-date]].

Common cases where the two dates diverge:

- **Weekend processing:** wire booked Friday, value-dated Monday
- **Check deposits:** booked today, value-dated T+2 or T+3 while the check clears
- **Back-dated corrections:** operations books today, value-dates to the correct past date. Interest accrual recomputes its window on the next run, so the days that correction covers are re-derived and the difference is posted as a true-up
- **Scheduled payments:** instruction booked now, value date is the 1st of next month

For [[payment-lifecycle|interbank payments]], the [[settlement-delay]] of the scheme (T+1 for SEPA Credit Transfer, T+2 for SEPA Direct Debit) determines the value date of the bank's own clearing-suspense leg — not the customer's, which value-dates to the debit itself regardless of scheme. Interest accrual always uses value date — using the wrong date would cause customers to earn too much or too little interest.`,
  },
  "balance-book": {
    title: "Book balance",
    body: `The **book balance** (also called the ledger balance) is the balance computed from all **posted transactions**, ordered by booking date. It is the raw sum of every debit and credit ever recorded against the account.

\`\`\`
Available Balance = Book Balance − Active Holds + Overdraft Limit
\`\`\`

Book balance can differ from [[balance-available|available balance]] when there are active [[holds]]: a €100 authorization hold reduces what you can spend but doesn't change the book balance until the hold is [[hold-capture|captured]] as a real posting.

It can also differ from the value-date balance: a forward-dated transaction is in the book balance before its economic effect begins. The balance endpoint returns both, and interest accrues on the value-dated one. The [[snapshot|end-of-day snapshot]] records the book balance alongside holds and available balance for each business day.`,
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

[[account-type-asset|Asset]] and [[account-type-expense|expense]] accounts hard-decline debits when available balance would go below zero — they have no overdraft. Liability accounts (customer deposits) can have a configured overdraft limit that extends available balance below zero, at the cost of [[overdraft-interest|daily interest]] once a rate is set on it.`,
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

When a liability account's book balance goes negative, the bank's perspective flips: economically it is now money owed *to* the bank. Once a rate is set, [[overdraft-interest|interest accrues daily]] on that drawn amount and is charged — capitalized — to the account monthly, which is what makes an overdraft **compound**.

That flip is never posted, which is the load-bearing point. The drawn amount stays exactly where it always was — the negative balance of this same Liability account — because it has no existence independent of that balance; there is no separate [[credit-facility|facility]] record for it. What looks like an Asset-side total is a derived aggregate over every deposit account, computed when asked, never stored.

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
| **Frozen** | Credits only — set by legal/fraud action |
| **Closed** | None — terminal, requires zero balance |

Transitions: Active ↔ Dormant (inactivity timer / incoming credit), any state → Frozen (freeze action), Frozen → prior state (unfreeze), Active → Closed (zero balance, no pending holds). **Closed is terminal** — the account cannot be reopened.

A **Frozen** account blocks card authorizations ([[holds]]) and every debit, but still accepts credits — the freeze implemented here is a **debit block**, which is the garnishment and fraud-investigation case: the customer cannot take money out while money owed to them keeps arriving. A *full* freeze, the sanctions case, blocks credits too; one status cannot express both, and this system implements the debit block. The freeze preserves the previous state so that unfreeze returns to Active or Dormant correctly.

The two directions are not mirror images. A debit can fail for want of money, so it is checked with an amount; a credit cannot, so the only question it answers is whether the account is still somewhere money may land — which makes **Closed** the one state that refuses one. Crediting a closed account would leave it holding money no withdrawal could reach and no second close could clear.`,
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

Examples: FedNow and SEPA Instant (via TIPS) settle each payment individually in central-bank money. Gross settlement offers **instant finality** but requires each bank to maintain sufficient reserves for every individual payment — unlike net settlement where offsetting payments cancel out.

Mind the difference between *speed* and *settlement model*: UK Faster Payments feels instant to customers yet settles on a deferred **net** basis behind the scenes — instant for the customer does not imply gross settlement.

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

These are the model's fixed delays. SEPA Credit Transfer really does settle by T+1; the **SDD T+2 is a simplification** — real SDD Core settles on the collection's *due date*, presented at least one business day ahead.

\`\`\`
Payment initiated today (T):
  Debtor leg, customer side  = T           (PSD2 Art. 87(2): no earlier than the debit)
  Debtor leg, suspense side  = T + settlement delay
  Creditor leg               = T + settlement delay
\`\`\`

The two sides of the debtor posting take effect on different days, which is why a [[value-date]] lives on the entry and not only on the transaction. The payer's money is gone the moment it is debited; the bank's clearing position settles days later.

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

- **Initiated → Accepted:** scheme validates (funds, [[mandate]] if needed); [[debtor-leg]] posted — payer's money moves into [[clearing-suspense]], the customer's side value-dated to the debit, the suspense side to settlement.
- **Accepted → Cleared:** clearing cycle closes; [[netting|net positions]] computed across all payments in the cycle. No money moves yet.
- **Cleared → Settled:** reserves move at the [[central-bank-reserves|central bank]]; [[creditor-leg]] posted — payee receives funds.
- **Rejected:** before clearing; [[reversal]] of the debtor leg restores the payer's balance.
- **Returned:** after settlement; an R-transaction fully unwinds the flow (available on [[allows-return|return-enabled]] schemes only).

See [[clearing-vs-settlement]] for why clearing and settlement are distinct phases, and [[settlement-delay]] for how the value date is set.`,
  },
  "debtor-leg": {
    title: "Debtor leg",
    body: `The **debtor leg** is the ledger entry that moves money out of the payer's account. It is posted at **acceptance** (when the scheme validates the payment), value-dated to the debit itself on the customer's side, and to the settlement date on the clearing-suspense side.

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
  Banks clear their clearing suspense accounts
\`\`\`

The gap between the two phases is the **settlement window** — during it, counterparty risk exists, and the money sits in each bank's [[clearing-suspense]] account. The [[payment-lifecycle]] reflects this: a payment moves Accepted → Cleared before it can reach Settled.`,
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
  Reserve at Central Bank (EUR) (Asset) ←── moves at settlement

Central Bank's chart of accounts:
  Reserve: Bank A (EUR) (Liability) ←── the other side
\`\`\`

These two accounts must always agree: when Bank A's reserve asset rises by €200, the central bank's Reserve: Bank A (EUR) liability also rises by €200. If they diverge, it signals a bookkeeping error. This is the [[double-entry]] invariant applied across the two institutions.

The reserve account is the ultimate destination of all [[net-positions|net settlement]] flows — no payment is final until it is reflected here.`,
  },
  "central-bank-reserves": {
    title: "Central-bank reserves",
    body: `The **central bank** holds one **reserve liability** per participant **per [[asset]]** — what it owes each member bank, in each kind of money it issues. This is the only place where commercial banks actually "meet" and where [[clearing-vs-settlement|settlement]] happens.

\`\`\`
Central-bank ledger:
  Reserve: Bank A (EUR)  (Liability)  ← CB owes Bank A its euro reserves
  Reserve: Bank A (USD)  (Liability)  ← ...and its dollar reserves
  Reserve: Bank B (EUR)  (Liability)  ← CB owes Bank B its euro reserves
  Settlement Assets (EUR) (Asset)     ← balancing asset when funded
  Settlement Assets (USD) (Asset)     ← one per asset, too
\`\`\`

Even the balancing account splits per asset: the euro reserves a central bank has issued are not backed by the dollars it has issued. See [[participant-assets]] for the matching split on each commercial bank's own books.

At settlement, the central bank transfers reserves between these liability accounts:

\`\`\`
Debit  Reserve: Bank A (EUR) (Liability) 20000  ← A's balance falls
Credit Reserve: Bank B (EUR) (Liability) 20000  ← B's balance rises
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

Events recorded, grouped by the layer (**scope**) that produced them:

- **ledger** — ledger/subledger/account creation, transaction posting, [[reversal]]
- **deposit** — account opened/frozen/closed, [[holds|hold]] creation, [[hold-release|hold release]], [[hold-capture|hold capture]], [[snapshot|end-of-day snapshot]]
- **payment** — participant added, [[mandate]] created/revoked, and every payment and [[clearing-vs-settlement|clearing cycle]] as it moves through the [[payment-lifecycle|lifecycle]]

Each event is written **inside the transaction of the operation it describes**, so an operation that rolls back leaves no record claiming it happened. The log is unbounded, so the API pages it: **limit** (default 100, max 1000) and **before**, an exclusive cursor on the event's sequence number.

\`\`\`
Example audit events (oldest first, as the API returns them):
  seq  when                  type                 entity      scope
  118  2024-03-01T08:55:00Z  hold.created         hold-xyz789  deposit
  119  2024-03-01T09:00:00Z  snapshot.taken       acct-001     deposit
  124  2024-03-01T14:32:00Z  hold.captured        hold-xyz789  deposit
  125  2024-03-01T14:32:01Z  transaction.posted   txn-abc123   ledger
\`\`\`

The trail enables: regulatory compliance (banks must maintain complete records), forensic investigation, incident response, and **independent balance verification** — you can replay all events to recompute any balance from scratch, cross-checking against [[snapshot|snapshots]].`,
  },
  snapshot: {
    title: "End-of-day snapshot",
    body: `An **end-of-day snapshot** captures an account's three balances — [[balance-book|book]], [[balance-holds|holds]], and [[balance-available|available]] — at the close of a business day. It is taken by the deposit layer, not the general ledger (which only computes book balance on demand). The figure it records is the ordinary **booking-date** book balance, not the [[value-date|value-dated]] one.

In the model this system describes, snapshots would serve four purposes — once the checkpointing they exist for is built:

1. **Interest accrual** — daily interest calculated on the end-of-day balance. For 4% APR on €10,000: \`€10,000 × 0.04 / 365 = €1.10/day\`.
2. **Statement generation** — monthly statements showing end-of-day balances, opening, and closing figures.
3. **Regulatory reporting** — regulators require daily position data.
4. **Performance** — balance queries starting from the latest snapshot and replaying only subsequent transactions, rather than the full history.

**What actually happens today:** a snapshot is written by \`TakeEndOfDaySnapshot\` and read back only by \`GetSnapshot\` and \`ListSnapshots\`. Interest accrual reads the value-dated entry list fresh on every run and never consults one; no balance query consults one either. What is captured today is the raw material a checkpoint would read from.

\`\`\`
Snapshot for 2024-03-01:
  account:   acct-001
  book:      €10,000
  holds:     €200
  available: €9,800
\`\`\`

The [[audit-trail]] records every snapshot event for complete auditability.`,
  },
  statement: {
    title: "Account statement",
    body: `A deposit account has **no ledger of its own**. Its statement is *derived* — every [[double-entry]] GL transaction that touches the account's backing GL account, projected onto that one leg, oldest→newest, with a running balance.

The running balance reconciles to the account's **book** balance: a built-in correctness check. Holds never appear here — they post nothing to the ledger until captured.`,
  },
  "statement-amount": {
    title: "Why credits add",
    body: `Your deposit is a **liability** to the bank — money it owes you. Its [[normal-balance]] is credit, so a **Credit increases** your balance (shown \`+\`) and a **Debit decreases** it (shown \`−\`).

Expand a row to see the full balanced transaction: your line is one leg; the contra account is where the money came from or went to — often the [[clearing-suspense]] account while a payment is in flight.`,
  },
  "relational-mapping": {
    title: "The ledger as relational tables",
    body: `The whole accounting model is six tables, and the shape of the [[double-entry]] rule is visible in them: a transaction is a **parent row** and its legs are **child rows**, so "a posting has two or more balanced entries" is a one-to-many relationship rather than two columns.

\`\`\`
ledgers ─┬─▶ subledgers ─┬─▶ accounts
         │               │
transactions ─▶ entries ─┘   (entry.account_id points at an account)
\`\`\`

Two details are easy to get wrong. \`entries\` needs an explicit **position** column, because \`Transaction.Entries\` is an ordered slice and a table has no order. And listings are ordered by \`(created_at, seq)\`, never by id — IDs are counter-derived strings, so \`dep_10\` sorts before \`dep_8\` and a customer list would silently reorder itself the first time a counter crossed a power of ten.

What is *not* a column is the balance — see [[derived-balance]]. What is not a single column is the primary key — see [[book-scoped-id]].`,
  },
  "derived-balance": {
    title: "A balance is an aggregate, not a column",
    body: `There is no \`balance\` column anywhere. A book balance is computed on demand by summing the account's entries, signed by [[normal-balance]] — so the account's *normal* direction is a parameter of the query, not a constant in it:

\`\`\`sql
SELECT COALESCE(SUM(CASE WHEN direction = $3 THEN amount ELSE -amount END), 0)
FROM entries WHERE book_id = $1 AND account_id = $2;   -- $3 = the account's normal direction
\`\`\`

Hardcoding \`debit\` there would be right for every asset and expense account and would negate every liability, equity and revenue one: a customer's checking account holding 750.00 would report −750.00.

A stored balance is a **cache of a derivable fact**, and caches go stale: any bug, crash or concurrent write that updates one of the two without the other leaves a number no one can reconcile. Deriving it means the [[audit-trail|append-only history]] is the single source of truth and the balance cannot disagree with it.

The cost is that reading a balance is an aggregate over every entry ever posted. The standard answer is not to add the column back but to checkpoint: a query would start from the nearest checkpoint and replay only what came after. The [[snapshot|end-of-day snapshot]] records a day's figure so that it *can* — but that is the design, not the current behaviour. No balance query in this system consults a snapshot; every one still sums the whole entry list.

Note also that a reversal is a *new, opposite* posting ([[reversal]]), so the sum still includes both — which is why the balance of a reversed transaction's account nets out rather than the row disappearing.`,
  },
  "unit-of-work": {
    title: "Unit of work",
    body: `A **unit of work** is one atomic scope — \`BEGIN\`, do everything, \`COMMIT\`, or \`ROLLBACK\` and it is as if nothing happened. Every mutation in this system runs inside one, and the [[audit-trail|audit event]] is written inside the *same* one, so a rolled-back operation leaves no record claiming it happened.

It has to span all three layers, because the operations do. Settling a [[clearing-vs-settlement|clearing cycle]] posts a [[creditor-leg]] in each member's book, moves [[central-bank-reserves|reserves]] in the central bank's book, and updates the payment and cycle rows — a partial success there would leave money that had left one bank without arriving at another.

\`\`\`
SettleCycle:
  BEGIN
    creditor legs in Bank A, Bank B, Bank C   (ledger layer)
    deposit balances updated                  (deposit layer)
    reserves moved at the central bank        (ledger layer)
    cycle + payments marked Settled           (payment layer)
    audit events appended
  COMMIT   ← all of it, or none of it
\`\`\`

Nesting one unit of work inside another is refused rather than allowed: the inner scope would be a *separate* transaction that commits even when the outer one rolls back. Methods come in pairs for this reason — the plain one opens a unit of work, the \`…Tx\` one joins the caller's.`,
  },
  "row-locking": {
    title: "Locking a row before you decide on it",
    body: `Checking a balance and then posting against it is two steps, and between them another transaction can post too — so both see enough money and together they overdraw the account. The check has to be locked to the write that depends on it:

\`\`\`sql
SELECT id FROM accounts
 WHERE book_id = $1 AND id = ANY($2)
 ORDER BY id
   FOR UPDATE;     -- held until COMMIT
\`\`\`

\`ORDER BY id\` is not cosmetic. Two transactions touching overlapping accounts in *different* orders would each hold a row the other wants — a deadlock. Taking locks in one agreed order means the second one simply waits.

This is exactly what a single process-wide mutex was doing for free in the in-memory store, which is why the hazard only becomes visible when the state moves into a database. See [[idempotency-race]] for the second race it was hiding, and [[unit-of-work]] for the scope the locks are held over.`,
  },
  "idempotency-race": {
    title: "Idempotency under concurrency",
    body: `An [[idempotency-key]] check written as *look, then insert* is not a check at all: two concurrent retries both look, both find nothing, and both insert.

The fix is to make the check and the write **one statement**, and let the database's own uniqueness rule decide:

\`\`\`sql
CREATE UNIQUE INDEX transactions_idempotency_key_idx
    ON transactions (book_id, idempotency_key)
    WHERE idempotency_key <> '';
\`\`\`

The insert is attempted; the loser gets a unique-violation error, which is translated back into the domain's "idempotency key already used". No window exists between deciding and acting, because there is no separate decision.

A reversal is closed the same way, with a **conditional update** whose row count is the answer:

\`\`\`sql
UPDATE transactions SET status = 'Reversed'
 WHERE book_id = $1 AND id = $2 AND status = 'Posted';
-- 0 rows → someone else already reversed it
\`\`\`

Both are instances of one rule: never read a value, decide, and write the decision — make the write itself the decision. Where that is impossible, lock first ([[row-locking]]).`,
  },
  "book-scoped-id": {
    title: "IDs are book-scoped",
    body: `Every bank in the network keeps its **own** book, and a chart-of-accounts number is unique *within* a book, not globally. Two participants both holding account \`200.100.001\` is normal, not a collision — so the primary key is the pair:

\`\`\`sql
CREATE TABLE accounts (
    book_id TEXT NOT NULL,
    id      TEXT NOT NULL,
    ...
    PRIMARY KEY (book_id, id)
);
\`\`\`

A single-column key would force globally unique numbering and destroy the [[ledger-vs-subledger|chart of accounts]] as a readable, per-bank structure. Every query is scoped the same way: a missing \`WHERE book_id = $1\` does not error, it quietly returns another bank's rows.

The [[payment-lifecycle|payment layer]]'s own entities — participants, payments, mandates, cycles, settlements — belong to no single bank, so they live in a network-wide book and are keyed by id alone.`,
  },
  asset: {
    title: "Asset (what an account is denominated in)",
    body: `An **asset** here is a unit of value that accounts are counted in — EUR, USD, BTC. It carries a \`code\`, a \`name\`, a [[asset-scale|scale]] and a class (\`Fiat\` or \`Crypto\`).

> **Not the same "asset" as [[account-type-asset|the account type]].** An account's *type* says whether it is something the bank owns or owes; its *asset* says what kind of money it is counted in. A euro deposit and a bitcoin deposit are both **Liability** accounts. (In Go the type is \`AssetDef\`, because \`Asset\` was already the account-type constant.)

Three properties do the work:

- **Defined in code, not stored.** The known assets are a list in Go, not rows in a table — an asset *definition* is a fact about the world, and "BTC has 8 decimals" is true in every book. Two books able to disagree about it would be a bug, not a feature. Same reasoning as [[scheme-asset|a payment scheme being a Go type]] rather than a row.
- **One asset per account, fixed at creation.** An account number and its currency are inseparable in real banking, which is why IBANs are per-currency.
- **So a balance stays a scalar.** If one account could hold several assets, every balance, statement line and snapshot would carry a *map* instead of a number. Binding the asset to the account pushes that dimension into the chart of accounts, where it costs one more account rather than one more parameter on everything.

A bank in three currencies therefore has three cash accounts, not one holding three kinds of money. An account naming a code the system does not know fails with \`ErrAssetNotFound\`.

What *is* per bank is which assets it operates in — see [[participant-assets]].`,
  },
  "asset-scale": {
    title: "Scale, and why it stops at 9",
    body: `An asset's **scale** is how many decimal places its minor unit has: 2 for EUR (cents), 8 for BTC (satoshi), 0 for an asset with no fractional unit. [[minor-units|Amounts are integers]] at that scale, so the same integer means different things in different assets — \`100000000\` is one bitcoin *and* a million euro.

Scale is capped at **9**, and every known asset is held to it.

The cap is arithmetic, not taste. An amount is an \`int64\`, which tops out near **9.22 × 10¹⁸**:

| Scale | Largest amount it can hold |
|---|---|
| 2 (EUR) | ~92 quadrillion € |
| 8 (BTC) | ~92 billion ₿ — the whole 21M supply is 2.1 × 10¹⁵ satoshi |
| 9 (the cap) | ~9.2 billion units |
| 18 (wei) | **9.2 units** |

At 18 decimals — ether's native precision — an \`int64\` holds 9.2 ETH. Not 9.2 billion: nine. Native 18-decimal precision and an \`int64\` amount are mutually exclusive, so an 18-decimal asset can only be carried here at reduced precision. Because the assets are a list in code rather than runtime input, the bound is **a test over that list** — adding an entry that would silently overflow fails the build.`,
  },
  "per-asset-balance": {
    title: "Transactions balance per asset",
    body: `[[double-entry|Debits must equal credits]] — but once a book holds more than one [[asset]], that has to hold **within each asset**, not across the whole transaction.

A global check is not a weaker rule, it is a broken one — and why becomes clear once you are precise about what it compares. An amount is an integer in its asset's [[asset-scale|minor units]] and carries nothing else, so a global sum is satisfied whenever the **integers** match, whatever they are worth. Ten billion is ten billion:

\`\`\`
Debit  Cash EUR (Asset)         10_000_000_000   // €100,000,000.00
Credit Customer BTC (Liability) 10_000_000_000   // ₿100.00000000
                                ──────────────
Total debits − credits:                      0   ✓ by the old rule
\`\`\`

It passes. The bank has booked €100,000,000 of cash against an obligation of 100 BTC — worth roughly €6.5 million — so about **€93 million appears out of nothing**, booked by an engine that had no idea it was pricing anything, because it was not: it was comparing two integers.

What did the damage is *not* that the legs were unequal in value; the check never looks at value, and has no rate with which to. It is that the integers were equal while the assets were not — EUR and BTC differ in scale by a factor of a million.

So the check sums debits and credits *within* each asset and requires each to net to zero. A failure returns \`ErrUnbalancedAsset\` wrapped with \`ErrUnbalancedTransaction\`, naming the asset; both match under \`errors.Is\`, so a caller can ask either "did it balance?" or "which asset broke?".

This is exactly why the ledger never needs an exchange rate: per asset, there is no rate to get wrong. A *global* multi-asset check would need one — which is how a pricing decision ends up inside an accounting engine. See [[fx-position-account]] for what that means for FX.`,
  },
  "fx-position-account": {
    title: "FX and position accounts",
    body: `**Not implemented** — there are no exchange rates and no trade operation in this system. This is the shape it was designed to accommodate.

[[per-asset-balance|Balancing per asset]] rules out the obvious posting: a customer selling €100 for bitcoin *cannot* be one transaction with a euro leg and a bitcoin leg, because that is precisely the posting the invariant rejects. Instead each asset balances through a **position account** of its own asset, and the trade is two ordinary postings:

\`\`\`
EUR side — balances in EUR:
  Debit  Customer EUR deposit   10000
  Credit EUR Position           10000

BTC side — balances in BTC:
  Debit  BTC Position          153000
  Credit Customer BTC deposit  153000
\`\`\`

Neither posting mentions the other, and **neither mentions a rate**. The rate lives entirely in the choice of the two amounts — 10000 cents against 153000 satoshi — made above the ledger by whatever quoted the price. The ledger records the consequence of a price; it never decides one.

The balance of a position account is then the bank's **open position** in that asset: how much it is long or short from the trades it has done. A bank that has bought and sold the same amount is *flat*, and the account reads zero.

What is **not** in the ledger under this scheme is trading profit and loss. That appears only when positions are revalued at a current rate — which needs a price, and so lives above the ledger too.`,
  },
  "scheme-asset": {
    title: "A scheme declares its asset",
    body: `Every [[payment-lifecycle|payment scheme]] names the one [[asset]] it carries. \`SCT\` and \`SDD\` both return **EUR** — SEPA is the *Single Euro Payments Area*, and a "SEPA dollar transfer" is not a thing. A scheme in another currency is another scheme, with its own rulebook and its own cycles.

Both the debtor's and the creditor's accounts are checked against it at initiation; a mismatch is \`ErrAssetMismatch\`. The check sits in \`InitiatePaymentTx\` rather than inside a scheme's own \`Validate\`, so it runs for **every** scheme — a future card scheme whose \`Validate\` places a hold instead of checking funds would otherwise skip it silently.

**The ledger cannot catch this at initiation**, which is the part worth understanding. A payment is never one posting: the [[debtor-leg]] is a transaction in the payer's bank's book, written now; the [[creditor-leg]] is a separate one in the payee's, written later at [[clearing-vs-settlement|settlement]]. Give Alice a euro account and Bob a bitcoin one, and initiation writes exactly this:

\`\`\`
Bank A:  Debit  Alice EUR     3000   ← balances in EUR ✓
         Credit Suspense EUR  3000
\`\`\`

Impeccable [[double-entry]]. Nothing in it contains the claim that a posting in another bank's book is its other half — and no ledger can see a claim that is not in front of it.

**It is caught at settlement, which is the argument *for* the check.** The creditor leg is built from the creditor's suspense account *in the scheme's asset*, so what actually gets posted is a EUR debit against a BTC credit:

\`\`\`
Bank B:  Debit  Suspense EUR  3000   ← EUR
         Credit Bob BTC       3000   ← BTC — does not balance
\`\`\`

That is \`ErrUnbalancedAsset\`. But settlement is all-or-nothing: one bad payment fails the **whole [[net-positions|clearing cycle]]**, and the error names an imbalance rather than the payment behind it. \`ErrAssetMismatch\` turns a late, batch-wide, misattributed failure into an immediate, correctly attributed one.

The general rule: an invariant is enforceable where the whole of it is visible, and cheapest where it is visible earliest.`,
  },
  "participant-assets": {
    title: "Internal accounts, one set per asset",
    body: `A participant bank's internal accounts — [[clearing-suspense|clearing suspense]], [[reserve-account|reserve at the central bank]], settlement — exist **once per [[asset]] it operates in**.

A bank clearing both a euro scheme and a dollar one holds two suspense accounts and two reserve accounts, not two currencies inside one. Partly because [[asset|an account is bound to a single asset]], and partly because [[net-positions|netting]] a euro position against a dollar one does not produce a smaller number, it produces a meaningless one.

\`\`\`
Bank A
├── EUR: suspense, reserve, settlement
└── USD: suspense, reserve, settlement
\`\`\`

[[clearing-vs-settlement|Settlement]] resolves the set from the **cycle's** asset — which comes from the cycle's [[scheme-asset|scheme]] — once for the whole batch. A member holding a net position but no accounts in that asset fails the entire settlement before anything posts, exactly as an underfunded member does.

There is deliberately **no fallback** to a default asset. Defaulting to euro would settle a dollar cycle in the wrong money, quietly, in the one place in the system where money becomes final.`,
  },
  "credit-facility": {
    title: "Credit facility",
    body: `A **credit facility** is a bank's extension of credit to a customer, tracked outside the demand-deposit layer: a [[term-loan|term loan]] (a fixed principal, disbursed once and repaid on a fixed schedule) or a [[revolving-line|revolving line]] (a reusable limit the customer draws down and repays repeatedly, billed in cycles).

An [[overdraft|overdraft limit]] extending a deposit account below zero is a THIRD form of credit in this system, but it is deliberately not a facility: it has no PRINCIPAL GL account, no schedule and no commitment — it is priced credit layered onto an existing liability account, not a standalone [[account-type-asset|asset]]. (It does get an accrued-interest receivable of its own, the moment a non-zero rate is set: interest earned is a real asset wherever it was earned.)

Every facility carries two GL [[account-type-asset|Asset]] accounts: a **principal** account (what is owed on drawn money) and an **interest** account (interest accrued and not yet collected). \`commitment\` is the ceiling the customer may draw against. \`drawn\` is DERIVED — the principal account's balance, never a stored field, the same discipline [[derived-balance|a book balance]] follows — and \`accruedInterest\` is \`Minor()\` of the facility's own exact [[accrued-interest|accrued-interest record]], which the interest account's balance always equals.`,
  },
  arrears: {
    title: "Arrears",
    body: `A facility's **arrears** are computed from its [[amortization|schedule]], not stored as a stream of events: they are a pure function of the schedule and a date, which is what lets a late payment, a corrected schedule, or a re-run end-of-day all just produce the right answer next time rather than needing to be replayed. The result IS stored — four fields on the facility, and the ones the API and this UI read — but as a cache recomputed at end of day and after every repayment, never as an accumulated tally.

Those [[days-past-due|days past due]] sort into five buckets: \`Current\`, \`1-29\`, \`30-59\`, \`60-89\` and \`90+\`. Reaching the top bucket is what makes a facility [[non-performing]]. A revolving line falls into arrears the same way a term loan does — by missing a billing cycle's minimum payment rather than a scheduled instalment, because [[capitalization|its instalments are cycles, not a fixed plan]].`,
  },
  amortization: {
    title: "Amortization schedule",
    body: `A term loan's **amortization schedule** is generated once, at disbursement, as a plan: one row per instalment, each carrying its own principal and interest due. It is built one of two ways, chosen when the loan is opened — see [[annuity]] and [[equal-principal]].

A row's outstanding amount is *that instalment's* unpaid remainder — (principal − paid principal) + (interest − paid interest) — not the loan's overall balance; the schedule is a plan the facility is compared against, and a [[repayment-allocation|repayment]] settles [[accrued-interest|accrued interest]] before touching it, which is not always what the schedule's own Interest column projected. A revolving line has no upfront schedule: its instalments are appended one per billing cycle, as it is [[capitalization|charged]].`,
  },
  "overdraft-interest": {
    title: "Overdraft interest",
    body: `An [[overdraft|overdraft limit]] is not free once a rate is set: interest accrues daily on the account's debit balance — the arranged rate up to the limit, and the [[unarranged-rate|unarranged rate]] on any balance beyond it — under a [[day-count|day-count convention]], the same way a [[credit-facility|facility]] does.

[[accrued-interest|Accrued interest]] is tracked at higher precision than the ledger posts (rounding to a whole minor unit every day would quietly lose a fraction of it); the account's accrued figure is the rounded amount the general ledger actually holds. It is charged to the account — [[capitalization|capitalized]] into the debit balance — on a billing cycle, which is why an overdraft sitting at its limit is not costless the way a \`0\`-rate account is.`,
  },
  "effective-dated-terms": {
    title: "Effective-dated terms",
    body: `A product's terms are a **timeline**, not a set of columns: one row per repricing, each carrying the day it takes effect, and none ever overwritten.

The reason is that every interest figure is a function of three things — account state, event history, and configuration. The first two are immutable and replayable here. If the third could be edited in place, that whole investment is undone, because "what did this account's product say on 15 July?" stops having a stable answer.

There is a sharper consequence than an audit weakness. While terms were mutable, the [[interest-accrual|accrual]] recompute could only reach back to the last repricing — reaching further would re-derive old days at *today's* rate. So a [[booking-date|back-dated]] posting landing before the last repricing was silently never trued up. With a timeline, every day is re-derived at the terms actually in force on it, so the window opens at account inception and the correction always lands.

Each row carries **two** dates, and the pair is the [[booking-date|booking-date/value-date]] distinction applied to configuration: when the repricing was *entered*, and when it takes *economic effect*. They can differ in either direction — a rate agreed on the 1st and keyed in on the 15th is backdated; a rate agreed for next month is future-dated and simply sits inert until the end-of-day runs reach it.`,
  },
  lending: {
    title: "Lending",
    body: `**Lending** is this system's third kind of credit relationship, alongside a plain deposit and an [[overdraft|overdraft limit]]: a [[credit-facility|facility]] whose drawn amount exists as a fact of its own rather than as another account's balance read by sign.

Two products ship: the [[term-loan|term loan]] (fixed principal, one disbursement, a fixed [[amortization|schedule]]) and the [[revolving-line|revolving line]] (a reusable commitment, drawn and repaid repeatedly, billed in cycles). Both accrue [[interest-accrual|interest daily]] on what is actually drawn, settle a [[repayment-allocation|repayment]] against interest before principal, and track [[arrears|arrears]] from their schedule rather than from a log of missed-payment events.

The [[overdraft|arranged overdraft]] is credit too, and older than either — but it is deliberately kept out of this package: its drawn amount is a current account's own negative balance, not an independent fact, so it carries no facility record, no schedule and no commitment.`,
  },
  "term-loan": {
    title: "Term loan",
    body: `A **term loan** is a fixed principal, disbursed once, repaid on an [[amortization|amortization schedule]] generated in full at disbursement — a mortgage or a personal loan. Disbursing debits the loan's Principal [[account-type-asset|Asset]] account and credits wherever the money goes (typically the customer's deposit account); nothing is owed before that posting, and everything scheduled is owed after it.

Its \`commitment\` — the original principal — never changes once disbursed. Compare the [[revolving-line|revolving line]], whose limit can be drawn against repeatedly rather than spent once.`,
  },
  "revolving-line": {
    title: "Revolving line",
    body: `A **revolving line** is a reusable commitment: the customer draws against it and repays, and — unlike a [[term-loan|term loan]] — can draw again against whatever headroom that repayment freed up. There is no schedule generated up front, because at opening there is nothing yet to schedule.

Instead, each billing cycle [[capitalization|capitalizes]] the interest accrued so far into principal and appends one [[amortization|instalment]]: that cycle's interest, plus a minimum-payment share of the newly-larger drawn balance. Missing that minimum payment — not missing a fixed instalment — is how a revolving line falls into [[arrears]].`,
  },
  annuity: {
    title: "Annuity amortization",
    body: `**Annuity** amortization gives every instalment on a [[term-loan|term loan]] the same total payment. Because the outstanding balance falls over the term, the interest share of that fixed payment falls with it and the principal share rises — early instalments are mostly interest, late ones are mostly principal. This is the shape of most retail mortgages and personal loans.

Compare [[equal-principal]], the other method this system offers; either can be chosen when the loan is opened, and the schedule's Interest column under both is only ever the SCHEDULED figure — see [[repayment-allocation]] for why a repayment settles something slightly different.`,
  },
  "equal-principal": {
    title: "Equal-principal amortization",
    body: `**Equal-principal** amortization repays the same slice of principal every instalment on a [[term-loan|term loan]]. Because interest is charged on a balance that falls by the same amount each time, the interest share — and so the total payment — shrinks every instalment, unlike [[annuity|annuity]] amortization's level payment. Common in commercial lending.

Under either method the LAST instalment is special: it repays whatever principal is actually left, so the schedule's principal column sums to the disbursed principal exactly, however the rounding fell along the way.`,
  },
  "interest-accrual": {
    title: "Interest accrual",
    body: `**Interest accrual** runs once per business day, on the drawn principal only — an undrawn [[credit-facility|facility]] or an in-credit account costs nothing. It is governed by a [[day-count|day-count convention]] that is a real term of the contract: the same balance at the same rate accrues a different amount under \`ACT/365\`, \`ACT/360\`, or \`30/360\`.

What accrues is held as [[accrued-interest|exact, sub-minor-unit interest]] on the facility or account record; what the general ledger posts each day is only the CHANGE in that record's rounded value, debiting the receivable and crediting Interest Income. A day on which the rounding does not tick over posts nothing at all.`,
  },
  "day-count": {
    title: "Day-count convention",
    body: `A **day-count convention** turns a pair of dates into a fraction of a year, and it is a real product parameter, not an implementation detail — the same balance at the same rate accrues differently under each:

- **\`ACT/365\`** — actual elapsed days over a 365-day year. Most retail lending.
- **\`ACT/360\`** — actual elapsed days over a 360-day year, so a year of daily accrual comes to 365/360 of the nominal rate. Euro money markets, US commercial lending.
- **\`30/360\`** — every month is treated as exactly 30 days and every year as 360, so a calendar month is *always* precisely a twelfth of a year.

That last property is \`30/360\`'s entire purpose: under \`ACT/365\` or \`ACT/360\`, a scheduled monthly instalment's interest and what actually [[interest-accrual|accrues]] over that month generally disagree — a 30-day month accrues less than a flat twelfth, a 31-day one more — and a [[repayment-allocation|repayment]] absorbs the difference in its principal portion. Under \`30/360\` the two always agree to the cent, which is exactly why real mortgages and bonds use it.`,
  },
  "accrued-interest": {
    title: "Accrued interest, at higher precision",
    body: `A day's interest on a real balance is mostly fraction — €10,000 at 6% (\`ACT/365\`) accrues 164.383561 cents a day, not a round number. Rounding that away daily, rather than keeping it, is a real annual error, so a facility or account holds its interest at TWO precisions simultaneously, and the split is the point:

- The **record** — micro-minor-units, minor units × 1,000,000 — is exact and never rounded.
- The **ledger** holds \`Minor()\` of it: the record rounded to a whole minor unit, half away from zero, because no posting can be a fraction of a cent.

Every day's posting is the CHANGE in \`Minor()\`, not the day's raw interest, so record and ledger can never drift apart even though most days post one value and occasional days post one more (or less) as the rounding ticks over. See [[capitalization]] for what happens to the residue when this figure is charged.`,
  },
  capitalization: {
    title: "Capitalization",
    body: `**Capitalizing** interest means charging the [[accrued-interest|accrued, rounded]] figure into principal rather than collecting it separately — \`ChargeInterest\` on a [[revolving-line|revolving line]], or the monthly charge on an [[overdraft-interest|overdraft]]. It is what makes either balance **compound**: next period's [[interest-accrual|accrual]] runs on a principal that already includes this period's interest.

Charging the ROUNDED figure rather than the exact one always leaves a residue of up to half a minor unit, in either direction — round down and the record stays slightly positive, round up and it goes slightly negative. \`Minor()\` of that residue is zero — except at an EXACT half of a minor unit, where it rounds AWAY from zero to ±1 even though the ledger is already at zero. That is why closing an account or facility tests the receivable's own ledger balance rather than the record: the record may legitimately disagree with the ledger by a sub-minor-unit amount, which is the entire reason it is kept at higher precision. Ordinarily the next accrual absorbs the residue as the balance moves again. A term loan is never capitalized this way — its interest is settled through its own scheduled instalments instead, never folded back into principal.`,
  },
  "repayment-allocation": {
    title: "Repayment allocation",
    body: `A repayment settles **interest before principal** — but the interest it settles is what actually [[interest-accrual|accrued]], not what the [[amortization|schedule]] projected for that instalment. On a €10,000 loan at 6%, the schedule's first-month interest is a flat twelfth: €50.00. Thirty days of \`ACT/365\` accrual come to €49.32 — a calendar month is never exactly a twelfth of a 365-day year. Paying the scheduled €193.33 credits €49.32 to the receivable and the rest, €144.01, to principal: 68 cents more than the schedule's own €143.33 assumed.

This is exactly why [[day-count|\`30/360\`]] exists: under it, schedule and accrual always agree, and a repayment never has to reconcile the two. The schedule stays a PLAN the facility is checked against; what is actually owed is always read from the accrued-interest record.`,
  },
  "days-past-due": {
    title: "Days past due",
    body: `**Days past due** is the calendar-day age of the OLDEST instalment on a facility's schedule that is still due and unpaid — always actual calendar days, regardless of the facility's own [[day-count|day-count convention]], because delinquency is a fact about the calendar, not about accrual.

The clock does not reset until that specific instalment is paid: a borrower who is permanently one payment behind stays visibly one payment behind, rather than looking momentarily current between due dates. These days sort into the buckets [[arrears]] tracks, and reaching 90 is what makes a facility [[non-performing]].`,
  },
  "non-performing": {
    title: "Non-performing",
    body: `**Non-performing** marks a facility once [[days-past-due|days past due]] reaches 90 — and it marks ONLY that. Nothing about how interest [[interest-accrual|accrues]], how it posts, or what the [[amortization|schedule]] says changes because of the flag.

A real bank's non-performing loan typically stops recognizing accrued interest into income (non-accrual accounting) and gets provisioned for expected loss (IFRS 9 / ECL). Both are real next steps for a production system and both are deliberately out of scope here — recorded as deferred work rather than silently skipped.`,
  },
  "unarranged-rate": {
    title: "Unarranged rate",
    body: `The **unarranged rate** is the interest charged on whatever part of an overdrawn balance sits BEYOND the arranged [[overdraft|overdraft limit]] — separate from, and higher than, the arranged rate charged up to it. An account can end up beyond its limit even though ordinary debits are checked against it: capitalizing interest can itself push a fully-drawn overdraft over, and a direct GL posting bypasses the check entirely.

Leaving that excess unpriced would make exceeding the limit free, which defeats the point of having a limit at all — so the unarranged rate exists specifically to make going beyond it cost MORE. It is a surcharge, not a switch: an account priced without one charges the arranged rate on the excess, never nothing. See [[interest-accrual]] for how the two rates combine over one accrual period.`,
  },
} satisfies Record<string, HintEntry>;

export type HintKey = keyof typeof hintContent;
