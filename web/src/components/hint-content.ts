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
    body: `A **ledger** is the top-level book of accounts — typically the **General Ledger (GL)** containing everything. A **subledger** is a subdivision of a ledger that groups related accounts: Customer Deposits, Loans and Advances, Bank Assets, Revenue.

What the classic design puts *inside* the deposit subledger is one account per customer, and what the GL carries is a single **control account** — one line standing for many — whose **stored** balance is supposed to equal their sum:

\`\`\`
General Ledger                      Customer Deposits subledger
└── Customer Deposits €10,000,000   ├── Alice Checking      €1,200
    (a control figure, written      ├── Bob Checking          €800
     down independently)            └── … 50,000 more accounts
\`\`\`

That is the same money counted in two places, so the two can drift — a bug, a partial failure, a timing window — and the bank runs a **subledger-to-GL reconciliation** every day to prove Σ(detail) equals the control figure. A mismatch is a break somebody has to investigate.

Here the folder holds **one line**, and a customer's account is not a row in the chart of accounts at all:

\`\`\`
General Ledger
├── Customer Deposits (subledger)
│   └── Customer Deposits (EUR)  (Liability, CONTROL)  ← 50,000 customers
├── Loans and Advances (subledger)
│   ├── Loan Principal (EUR)     (Asset, CONTROL)      ← every borrower
│   └── Accrued Interest (EUR)   (Asset, CONTROL)
├── Bank Assets (subledger)
│   └── Cash Vault (Asset)
└── Revenue (subledger)
    └── Fee Income (Revenue)
\`\`\`

It is still a control account — one line standing for many — but it is also the only place the money is recorded. Every entry against it names *whose* money the leg is: the **holder**, recorded in \`subsidiary_id\`. Alice's balance is that line's balance filtered to Alice; total customer deposits is the same sum with the filter dropped. So Σ(detail) = control is not a nightly proof but one statement read two ways, and there is no second number to reconcile against. What it costs is speed — every balance is still added up from entries.

What the control account buys is the chart of accounts itself: a bank with fifty thousand customers and ten thousand loans has a chart of a few dozen lines, one per asset per role plus its own positions, and its trial balance is a page rather than a book.`,
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

The API always sends and receives an integer at the asset's scale. The console is responsible for converting to and from display format, given the asset (and its scale) the amount belongs to. Never send a decimal like \`30.00\`.`,
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

The limit is [[effective-dated-terms|effective-dated]], so the figure this formula uses is the one in force on the day being asked about — not a column that can be edited out from under a past calculation. It is also [[pinned-vs-floating|pinned to the account]]: unlike the rate, it never comes from the [[product-catalogue|product]], because how far *this* customer may go overdrawn is an underwriting decision about them.

When a liability account's book balance goes negative, the bank's perspective flips: economically it is now money owed *to* the bank. Once a rate is set, [[overdraft-interest|interest accrues daily]] on that drawn amount and is charged — capitalized — to the account monthly, which is what makes an overdraft **compound**.

That flip is never posted, which is the load-bearing point. The drawn amount stays exactly where it always was — the negative balance of this same Liability account — because it has no existence independent of that balance; there is no separate [[credit-facility|facility]] record for it. What looks like an Asset-side total is a derived aggregate over every deposit account, computed when asked, never stored.

[[account-type-asset|Asset]] and [[account-type-expense|expense]] accounts always **hard-decline** — they refuse for insufficient balance rather than going negative. Only deposit (liability) accounts support an overdraft limit.`,
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

The two directions are not mirror images. A debit can fail for want of money, so it is checked with an amount; a credit cannot, so the only question it answers is whether the account is still somewhere money may land — which makes **Closed** the one state that refuses one. Crediting a closed account would leave it holding money no withdrawal could reach and no second close could clear. A settled payment for a closed account is therefore credited to the bank's [[unclaimed-balances|unclaimed balances]] instead.

Status governs what the account may do; it says nothing about how a counterparty finds it in the first place — that is a separate concern, see [[account-addressing]].`,
  },
  "scheme-direction-push": {
    title: "Push scheme",
    body: `In a **push** scheme the **payer's bank initiates** and sends the funds to the payee's bank. No [[mandate]] is needed because the payer is voluntarily pushing money out.

**SEPA Credit Transfer (SCT)** is the canonical push scheme: Alice instructs Bank A to credit Bob at Bank B. Bank A validates its own half, posts the [[debtor-leg]], and sends a \`pacs.008\` onward. Bank B checks Bob's account and answers; the clearing house is what then takes the payment into a cycle.

\`\`\`
Direction of flow:
  Alice (debtor) → Bank A → clearing → Bank B → Bob (creditor)
  Submitted by:  Alice / Bank A   (the payer side)
\`\`\`

Money always flows **debtor → creditor** regardless of who initiates. [[scheme-direction-pull|Pull schemes]] reverse who *triggers* the instruction, but the underlying posting choreography is identical — one posting function serves both, and whoever runs it is the debtor's bank.

\`Direction\` decides three things, and none of them is which way money flows: **which bank submits** (the payer's on a push, the payee's on a pull), **which half of the checks runs at submission and which on receipt**, and **whether a [[requires-mandate|mandate]] is required** — which, because the creditor holds the mandate in SEPA, also decides which bank checks it.`,
  },
  "scheme-direction-pull": {
    title: "Pull scheme",
    body: `In a **pull** scheme the **payee's bank initiates** the collection by presenting a payment instruction to the payer's bank. The payer must have previously signed a [[mandate]] authorising this specific creditor to collect.

**SEPA Direct Debit (SDD)** is the canonical pull scheme: a utility company's bank submits a collection against Alice's account at Bank A. Bank B — the *creditor's* bank — validates the [[mandate]], because in SEPA the creditor is who holds it, and its submission posts **nothing**: Alice's account belongs to another bank and Bank B has never seen it. Bank A posts the [[debtor-leg]] when the collection reaches it, which is the first moment anyone in the system can look at the account being collected from.

\`\`\`
Direction of flow:
  Alice (debtor) → Bank A → clearing → Bank B → Utility (creditor)
  Submitted by:  Utility / Bank B   (the payee side)
  Debited by:    Bank A            (on receipt)
\`\`\`

Money still flows **debtor → creditor** — "direction" only means who triggers the instruction. Because the creditor initiates, [[requires-mandate|a mandate is required]], and the debtor — who never asked for the debit — can dispute the collection and have it [[allows-return|returned]]. Returns are not a pull-only thing, though: both SEPA schemes allow them here, and direction decides only *which* bank sends one.`,
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

[[netting]] reduces the volume of central-bank reserve movements dramatically. Once each bank has booked its own half of the cut-off — which is after [[settlement-finality|settlement]], not at it — both banks' [[clearing-suspense]] accounts are back to zero. Compare to [[settlement-model-gross|gross settlement]], where each payment settles individually.`,
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

A scheme here can declare itself gross, but nothing settles a payment one at a time: no gross path is built. Net schemes ([[settlement-model-net]]) are fully operational.`,
  },
  "requires-mandate": {
    title: "Requires a mandate",
    body: `**Requires a mandate** flags whether a payment scheme demands a pre-signed [[mandate]] before a collection can proceed.

[[scheme-direction-pull|Pull schemes]] (e.g. SEPA Direct Debit) require a mandate because the **payee initiates** the debit — somebody has to hold proof the payer consented. In SEPA that somebody is the **creditor**, so it is the **creditor's bank** that checks the mandate, synchronously, when it submits the collection. The checks: mandate exists, is active, the creditor matches, the debtor matches, and the amount is within the limit. Any failure and the collection is refused there and then, as an error to the caller — it never becomes a message, and the payer's bank never hears about it.

[[scheme-direction-push|Push schemes]] (e.g. SEPA Credit Transfer) do **not** require a mandate — the payer is voluntarily pushing money, so no standing authorisation is needed.

\`\`\`
SDD mandate check — run by the CREDITOR's bank, at submission:
  1. Mandate exists?          ✓
  2. Mandate active?          ✓
  3. Creditor matches?        ✓
  4. Debtor matches?          ✓
  5. Amount ≤ mandate limit?  ✓  → the pacs.003 goes out
  Any ✗ → ErrMandateRequired / ErrMandateRevoked / ErrMandateExceeded
          refused to the caller; nothing is posted and nothing is sent
\`\`\``,
  },
  "allows-return": {
    title: "Allows return",
    body: `**Allows return** flags whether a settled payment can be unwound by an **R-transaction** (return, recall, or refund). This is a scheme property — not all schemes permit it.

SEPA Direct Debit [[allows-return|allows returns]] because the debtor did not initiate the collection and may dispute it. A return posts compensating entries that move funds back from creditor to debtor across the central bank — one customer leg in each bank's own book, plus the reserve reversal between them, in **three units of work at three institutions** joined by messages. Nothing is deleted; the original entries stay exactly as posted.

\`\`\`
1. the returning bank posts its own leg
      ← the last moment anyone can say no
2. --pacs.004--> CSM --pacs.004--> central bank
3. the central bank reverses the reserves
      ← FINAL here
4. --camt.053--> BOTH banks
      ← sent BEFORE the answer, and that matters
5. --pacs.002--> CSM --pacs.002--> the returner
6. the CSM releases the held pacs.004 to the
   other bank

each bank then COLLECTS, the settlement
agent first: it books its reserve mirror from
(4), and the other bank goes on to post its
customer leg from (6) — the second leg, and
what turns the payment Returned
\`\`\`

The clearing house **holds** that \`pacs.004\` between steps 2 and 6 rather than relaying it straight through: a bank that posted its leg against a return the settlement agent then refused would have moved a customer's money for nothing. On an \`RJCT\` the held message is dropped and only the answer goes out. The returning bank then **unwinds its own leg** by [[reversal|reversing]] the posting, so its customer is back where the return found them.

And then the return can be **asked again**. The commonest \`RJCT\` on this path is \`AM04\` — the other bank was short of reserves at that moment — and a shortfall somebody can cover is not a payer who has lost their refund right. The reversed leg's transaction id stays written on the payment, because the retry's idempotency key is derived from it; what it stops meaning is *this bank's leg stands*. That answer is in the ledger, on the transaction. Reading the id alone let a retried return run its whole conversation — reserves reversed, the other bank's customer clawed back, \`ACSC\` on the wire — around a refund that no longer existed.

Because the settlement agent is final at step 3 and each bank books afterwards, a return has an [[unreconciled-position|unreconciled position]] exactly as a cut-off does — and the bank's own advice row for it is keyed by a **payment** id where a cut-off's is keyed by a cycle id.

Which leg lands where never changes: the **clawback** is always at the creditor's bank, the **refund** always at the debtor's. Both customers are put back where they were when both accounts can still take the posting; where one cannot, the money goes to that bank's [[unclaimed-balances|Unclaimed Balances]] or comes out of its [[returns-receivable|Returns Receivable]] rather than stranding.

**Both** SEPA schemes allow returns here — \`SCT\` and \`SDD\` alike — and a return is refused only for a scheme that permits none. That is not a shortcut — a credit transfer return is a real R-transaction too, sent by the *beneficiary's* bank when it cannot apply the funds (a closed account, a name that does not match). Which bank sends it is what direction decides: always the bank that **received** the original instruction, so the payee's bank on a push and the payer's on a pull.

What a credit transfer has no equivalent of is the debtor's **refund** — the 8-week, no-questions-asked claim on a settled collection. A payer who simply changes their mind about a credit transfer must ask for a *recall*, which the beneficiary can refuse; that is a \`camt.056\`, and this system does not implement it. Two simplifications inside the return itself: it settles immediately rather than being batched into a later R-cycle, and no return window is enforced — a return asks whether the scheme allows one and whether the payment is settled, and nothing asks how long ago it settled. What it *does* ask about the money depends on direction, and on one rule: **a bank can refuse a leg only if it posts it before it sends.**

| | the returning bank posts, before it sends | the other bank posts, after finality |
|---|---|---|
| **push** (SCT) | the **clawback** — refusable, so no \`pacs.004\` exists | the **refund** — always postable |
| **pull** (SDD) | the **refund** — always postable | the **clawback** — forced; [[returns-receivable|Returns Receivable]] |

So on a credit transfer the payee's bank can refuse a clawback it cannot fund, as an error to the caller and no message at all; on a collection the creditor's bank hears about the return only after the reserves have moved, so it forces the clawback — the payer's 8-week right is unconditional, and the creditor's bank carries the credit risk. That is why creditor banks vet their creditors.`,
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

"Business day" in that table is now the [[target-calendar|settlement calendar's]] answer rather than a calendar day wearing the name: a Friday payment does not settle on Saturday, because no [[business-day|business day]] clears one.

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

The mandate records: **creditor ID**, **debtor account ID**, **maximum amount**, and **status** (active / revoked). The creditor's bank validates all four when it submits the collection, because in SEPA the creditor is the party that holds the mandate:

\`\`\`
Mandate checks, at the CREDITOR's bank, on submission:
  creditor_id   == payment.creditor?    ✓
  debtor_id     == payment.debtor?      ✓
  status        == active?              ✓
  amount        ≤ mandate.max_amount?   ✓
  → the collection is sent, and the payment is Initiated
\`\`\`

A revoked mandate causes immediate rejection, and so does an amount over the limit. Passing all four does **not** make the payment [[payment-lifecycle|Accepted]] — it leaves it *Initiated*, in no cycle, waiting for its bank's cut-off; only the clearing house accepts, and the payer's bank is handed the collection later still, once the cycle has settled. Once a payment is settled, the debtor can trigger a [[allows-return|return]] to dispute the collection.

**The mandate is the creditor bank's row, and the API says so.** A *network-level* resource — created and listed on the clearing house's console, with every member's authorisations over every other member's customers' accounts on one page — would be the wrong owner for the rule stated above: the bank that validates a mandate is the creditor's, so the record belongs to it. Creating one is a request to that bank, and what it lists is its own customers' authorisations. Two consequences follow and neither is cosmetic. The **debtor is recorded, not checked** — that account is at another bank, and no bank here reads another's register — so a mandate naming an account that does not exist is created and fails its first collection. And the mandate's **asset comes from the creditor's account**, the one the recording bank holds; two accounts in different assets are no longer refused at creation, because the comparison needs a register this bank cannot read. That collection is refused at first use instead, by the debtor's bank, on the [[scheme-asset|scheme's asset]] — later than before, which is the honest price of one bank keeping only its own books.`,
  },
  "payment-lifecycle": {
    title: "Payment lifecycle",
    body: `Every payment travels through an explicit state machine with clearly separated clearing and settlement phases.

\`\`\`
Initiated ──▶ Accepted ──▶ Cleared ──▶ Settled
     │            │                        │
     └────┬───────┘                        ▼
          ▼                             Returned
      Rejected
\`\`\`

**It is a state machine per COPY, not per payment.** A payment is [[store-split|three rows in three databases]] — the payer's bank's, the payee's bank's and the clearing house's — and no one of them can read another's. The three legitimately disagree about the status, because each holds what *it* was told and the clearing house holds what it decided. The \`ACCP\` goes only to the bank waiting for an answer to its instruction, so the bank that *answered* it is never told and its copy reads *Initiated* until settlement: \`Initiated → Settled\` is a legal edge at a bank and an impossible one at the clearing house.

Every arrow is drawn by a **named institution**, and no two adjacent ones by the same:

- **Initiated** is a state a payment sits in, not a moment. The submitting bank has run its own half and the instruction is waiting in that bank's [[payment-hub|hub]] for its next cut-off — nothing has been sent, and nobody else has heard of it. On a push that half already posted the [[debtor-leg]] — the payer's money is in [[clearing-suspense]], customer's side value-dated to the debit and suspense side to settlement — while the payment still reads *Initiated*. On a pull it posted nothing, and the **payer's** bank posts the debtor leg when the collection reaches it, still leaving the payment *Initiated*.
- **Initiated → Accepted:** the **clearing house's** act and nobody else's — it validates the [[bulk-file|file]] it was uploaded and takes each transaction into the open cycle for its scheme. No open cycle is a refusal, and it travels as **TM01, "invalid cut-off time"**: the cut-off belongs to the clearing house, so the refusal does too.
- **Accepted → Cleared:** the cut-off. [[netting|Net positions]] computed across all payments in the cycle. No money moves.
- **Cleared → Settled:** three institutions, and the arrow is the *third* one's. The clearing house asks — closing a cycle sends a \`pacs.009\` — and the **central bank** discharges the net positions in its own book, which is where the money becomes [[settlement-finality|final]]. The payment row moves to Settled later still, when the **payee's own bank** posts the [[creditor-leg]] on being told, per payment, that the cycle settled. The gap between those two moments is the [[unreconciled-position|unreconciled position]]. A net payer who cannot cover is refused before anything posts at all: the cycle stays Closed with no settlement against it, and every payment in it stays Cleared.
- **Rejected:** reachable from *Initiated* as well as *Accepted*, and it is **two halves in two units of work** — the clearing house marks the payment Rejected, and the submitting bank then [[reversal|reverses]] the debtor leg. In between, the rejection has half-happened: the payment reads Rejected while the customer's money is still in suspense. **The clearing house is the only institution that rejects anything**, and one message is all a rejection needs: before the cycle settles, the bank that submitted is the only bank that has heard of the payment. A receiving bank cannot reject — it is handed the instruction with the money already in its clearing suspense, so what would have been \`AC01\` or \`AM04\` is a return instead. *Before settlement, reject; after settlement, return* is the rule book's own division, and settling before releasing is what puts every receiving bank's objection on the second side of it.
- **Returned:** after settlement; an R-transaction unwinds the flow (available on [[allows-return|return-enabled]] schemes only), and it is **three acts at three institutions**. It is sent by the bank that *received* the original instruction, and that bank posts the leg it owns **before** it sends — which is the only reason it can still refuse. The central bank then reverses the reserves and is final. The other bank posts the leg it owns **after** that, on the \`pacs.004\` the clearing house releases to it, and cannot refuse: there is nothing left to refuse. The status turns Returned when the second customer leg lands.

A member bank advances its own copy through exactly three acts of its own: recording that the payment reached a cycle (which posts nothing — no money moves at an acceptance, but a record that jumped from *instructed* to *settled* could not answer the question a customer asks in between), recording a rejection **and** reversing the debtor leg in one unit of work, and recording a settlement while releasing the payee's money if it holds the payee. A *receiving* bank does the last of those the moment it is handed the instruction, because by then the payment is already final.

See [[clearing-vs-settlement]] for why clearing and settlement are distinct phases, and [[settlement-delay]] for how the value date is set.`,
  },
  "download-queue": {
    title: "Nothing is pushed at a bank",
    body: `A bank does not receive its results. It **collects** them.

The file-transfer protocol between a bank and its clearing house — **EBICS**, which is what German and French banks actually use — has no push at all. The subscriber is always the client, so the topology of a whole network is short:

\`\`\`
member bank    -->  clearing house      (files up, results down)
member bank    -->  settlement agent    (lodgements up, statements down)
clearing house -->  settlement agent    (net positions up, the answer down)
\`\`\`

The settlement agent dials nobody. Every result waits in a **download queue** — the files one host is holding for one subscriber, in the order they were put there — until that subscriber comes and asks.

**The queue is the routing table.** The clearing house routes a file to a receiving bank by putting it in that bank's queue. There is no address to look up and no table of who-is-reachable that could disagree with who-is-a-member, because being enrolled is what creates the queue in the first place. A BIC with no queue is a BIC the clearing house refuses to route to, out of a row it holds itself.

Two things follow that a push-based network cannot express. **An upload can genuinely fail** — a refused connection, a timeout — and the uploading bank keeps its file and tries again. And **a bank that never collects** is a real operational failure with a real remedy: its customers are simply never told the fate of anything, while the queue grows.

An upload is answered **technically and immediately** — the file arrived, and here is an order id for it — and never with what the file *means*. That answer is a status report the sender collects on a later download. Asking "what became of my order" in between is its own request: *received, not yet processed* is a distinct answer from *accepted* and from *no such order*.

The identity a subscriber sends is a header, and it is **not** authentication. A real EBICS connection signs the order, encrypts the payload and authenticates the request under three separate key pairs, and enrolment ends in a hand-signed paper letter carrying the key hashes. It is the most heavily authenticated hop in the whole chain, and none of that is modelled here.

See [[bulk-file]] for what a file carries and [[payment-hub]] for where one comes from.`,
  },
  "bulk-file": {
    title: "One file in, many files out",
    body: `Banks do not exchange payments one at a time. They exchange **files**.

One \`pacs.008\` carries every credit transfer its sending bank accumulated before its [[payment-hub|cut-off]] — thousands of them in a real scheme. The clearing house answers that file with **one** \`pacs.002\` carrying a decision per transaction, whose group status is **\`PART\`** when they did not all go the same way: some accepted, some rejected, in one document.

Then it does the thing a clearing house exists for. It **sorts the file by creditor agent** — the element naming the bank that holds each payee — and builds one output file per receiving bank:

\`\`\`
one bank uploads:      1 file, N transactions
the clearing house:    1 answer back to the sender
                       M files out, one per receiving bank
\`\`\`

So none of the institutions in a chain ever sees the same file, and that fan-out is invisible in any system where a message carries one payment. On a [[scheme-direction-pull|pull]] the sort is by the **debtor's** agent instead — a collection travels towards the money's source while a transfer travels towards its destination — and that single element is the whole difference at the clearing house.

It sorts **without reading any record of its own**. A clearing house that had to look a payment up to decide where to send it could not route a file about a payment it does not hold, which in a real network is most of them.

The M files do not leave at once. Each waits until the cut-off carrying its transactions has settled — see [[business-day]] for the phases that put those two in that order — so the clearing house is holding a **share** per receiving bank in between, and it keeps them in its own database (\`held_files\`, and \`held_file_transactions\` for which of the uploaded file's transactions are whose). What is kept is the **submitting bank's file plus the positions in it**, not a rendered output file: a payment an operator rejects out of the open cut-off has to be cut out of the share when the rest of it settles, which a finished file could not do. And it has to be kept somewhere that outlives a process, because [[settlement-finality|reserves move]] when the cut-off settles: an institution that lost its shares in between would leave the money final and the payee's bank never told the payment exists.

What is *not* batched here is the customer's half: instructions arrive one at a time, so a company handing its bank a single file carrying its whole payroll has no equivalent — that is the customer-to-bank layer, and this system does not model it.

See [[download-queue]] for how a file reaches the bank it is addressed to.`,
  },
  "payment-hub": {
    title: "The hub, and the cut-off",
    body: `When a customer instructs their bank, **nothing is sent**.

The bank validates the instruction, posts the [[debtor-leg|debtor leg]] on a push, and puts the payment in its **hub** — the pile of its own customers' instructions waiting for the next [[bulk-file|file]]. The payment is *Initiated*, in no cycle, and the payee's bank has never heard of it. That is a state it rests in rather than passes through, because nothing in a bulk scheme happens on a timer.

The **cut-off** is what empties the hub: one file per scheme, uploaded to the clearing house, answered with an order id. Everything a bank accumulated since its last cut-off goes in one file, and everything instructed a moment later waits for the next one.

**Two cut-offs share the word and they are not the same act**, which is worth keeping straight:

| Whose | What it does |
|---|---|
| a **bank's** | turns its hub into files and uploads them |
| the **clearing house's** | closes every open cycle, [[netting|nets]] it, and instructs settlement |

They are two steps apart in a [[business-day|business day]] — every bank cuts off first, the clearing house validates and accepts what arrived, and only then does it cut off in its own sense.

The customer-facing half of the door does not move: an instruction that fails the **submitting bank's own** checks — no funds, an account that is not the customer's, a revoked [[mandate]] — is refused there and then, before it ever joins the hub. What waits is only what that bank has already agreed to carry.

The hub is not a durable record. A payment in it has a committed debtor leg, so its money is already in [[clearing-suspense|clearing suspense]] — which is exactly where a reconciliation would find it if the hub were ever lost.`,
  },
  "business-day": {
    title: "The business day",
    body: `A bulk payment scheme is **store-and-forward against a clock**. Banks accumulate, a cut-off arrives, files move, and results come back later. So the unit of progress is not a message — it is a **day**.

Running one carries every payment through every phase, in a fixed order, for every institution before the next phase begins:

\`\`\`
0  banks   take the published routing directory
1  banks   cut-off -> one file per scheme, uploaded
2  csm     validate, accept into the open cycle, answer per transaction,
           and BUILD each receiving bank's share -- releasing nothing
3  csm     its own cut-off: net every open cycle, instruct settlement
3b csm     discharge the cut-offs that instructed nobody
4  cb      settle whole-or-nothing, statement per member, answer the csm
5  csm     collect the answer -> RELEASE the output files
6  banks   collect -- THE SETTLEMENT AGENT FIRST, then the clearing house
7  banks   end of day: interest accrual, arrears
   clock   -> the next calendar day
\`\`\`

It **does not interleave**. Each phase finishes everywhere before the next starts, because real clearing is exactly this batched.

Three of those orderings are load-bearing:

- **The directory refresh is first**, so a bank admitted since the last day can be paid by its neighbours today rather than whenever somebody remembers to pull. See [[routing-directory]].
- **Phases 3, 4 and 5 are settle, then release.** The cycle settles before any output file leaves the clearing house, so a receiving bank is handed its instructions only once the funds behind them are final. Reverse them and a bank could credit a customer against a batch that still fails. What the order costs is the receiving bank's ability to reject — its objections become [[allows-return|returns]] instead.
- **Banks collect from the settlement agent first.** The reserve mirror has to be booked before the [[creditor-leg|creditor legs]] draw on it, and the two files sit in **different queues at different institutions**. Two connections share no ordering, so nothing about the order they were written in survives; what guarantees it is the bank's own collection order, which is a decision each bank makes about its own operations.

**A cut-off can net to nothing, and one that does is discharged by the clearing house itself.** If every member's position cancels — or the scheme saw no traffic at all — there is nothing to instruct, because a settlement instruction with no transaction is not a message. Nobody is asked, so no answer comes back and phase 5 would wait for ever; the institution that netted the batch settles it where it stands. No reserves move and none need to: each bank's [[clearing-suspense|clearing suspense]] is emptied by the payments it receives in the same batch, and **no settlement is recorded anywhere** for such a cut-off.

A **failure never stops the day.** A file one bank cannot read must not stop another bank being paid, so every phase records what went wrong and carries on. What a day hands back is a report: the files that moved, what was decided about each transaction, and every file some institution could not get through.

Which days clear at all is [[target-calendar|the settlement calendar's]] answer.`,
  },
  "target-calendar": {
    title: "Which days money can move",
    body: `Clearing and settlement run on the **settlement agent's** calendar — in the euro area, **TARGET**, the Eurosystem's real-time gross settlement system. It is not a currency's calendar, not a market's, and not any one country's.

That distinction has teeth. German banks are shut on 3 October and TARGET is not, so a payment submitted that morning clears and settles that afternoon: a national holiday closes branches, it does not close the settlement agent.

TARGET is shut at weekends and on six named days:

\`\`\`
New Year's Day   1 January
Good Friday      moves with Easter
Easter Monday    moves with Easter
Labour Day       1 May
Christmas Day    25 December
St Stephen's Day 26 December
\`\`\`

Two of the six move with Easter, which is arithmetic rather than a list somebody maintains. And TARGET **substitutes nothing**: a 25 December falling on a Saturday is simply a closing day the year does not get, unlike the UK and US calendars, which move the observance to the following Monday.

**A day the scheme is shut still happens.** The date advances and every bank still runs its end of day, because [[interest-accrual|interest accrues]] over a weekend — that is the entire reason [[day-count|day-count conventions]] exist. What does not run is any cut-off, any clearing and any settlement.

This is what makes a rule book's deadlines mean what they say. SEPA gives a receiving bank **three banking business days** to return a credit it cannot apply; counted in calendar days, a balance arriving on a Thursday is overdue on Sunday where the rule book would say Tuesday. See [[unclaimed-balances]].

What is **not** modelled: there is no time of day inside a settlement day, and one cut-off per day where SEPA runs several settlement cycles. A calendar has to exist before a time within it means anything.`,
  },
  "debtor-leg": {
    title: "Debtor leg",
    body: `The **debtor leg** is the ledger entry that moves money out of the payer's account. It is posted by the payer's **own bank** — the rule that covers both directions — value-dated to the debit itself on the customer's side, and to the settlement date on the clearing-suspense side.

*When* that bank posts it is what the [[scheme-direction-push|direction]] decides. On a push it submits, so it posts at submission. On a [[scheme-direction-pull|pull]] it *answers*, so it posts when the collection reaches it — which is the first moment any actor in the system can see the account being collected from. Either way the payment is still **Initiated** afterwards: acceptance is the clearing house's act, and it comes later.

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
    body: `The **creditor leg** is the ledger entry that delivers funds into the payee's account. It is posted by the **payee's own bank**, on the instruction the clearing house releases to it once reserves have actually moved between banks at the [[central-bank-reserves|central bank]]. That bank sees the payment for the first time then: the file is held until the cut-off is final, so no bank ever credits a customer against money that has not settled.

Two postings put that bank back to flat, and they are **two separate [[unit-of-work|units of work]], booked from two different institutions' messages**. The reserve mirror comes **first**, and what guarantees it is the bank's own [[download-queue|collection order]] — it takes the settlement agent's queue before the clearing house's, because two connections share no ordering and nothing about who wrote first survives:

\`\`\`
Bank B — reserve mirror, from the camt.053:
  Debit  Reserve at CB (Asset)         300  ← Bank B's reserve asset rises
  Credit Clearing Suspense (Liability) 300  ← still owed to the payee
\`\`\`

\`\`\`
Bank B — creditor leg, from the released pacs.008 (Bob gets €300):
  Debit  Clearing Suspense (Liability) 300  ← suspense cleared
  Credit Bob Deposit (Liability)       300  ← Bob's balance rises
\`\`\`

Written as one four-entry posting it says two wrong things at once. It claims **one** institution's message moved both, when the whole [[nostro-reconciliation|nostro/vostro check]] is that two different senders did — a bank checking one sender against itself has reconciled nothing. And it erases the [[unreconciled-position|unreconciled position]], which *is* the interval between these two postings: the reserves have moved and the payee has not been paid.

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

A payment with **one bank at both ends** has nothing to clear, and it is refused rather than carried: both customers bank at the same institution, so no interbank obligation comes into existence, and there is no position to net and nothing to settle. The bank recognises the beneficiary as its own and moves the money between two of its own deposit accounts instead — a [[book-transfer]], which never reaches a scheme. The refusal is a statement about the **route** and not about the payment, and it names the route that does carry it.

The gap between the two phases is the **settlement window** — during it, counterparty risk exists, and the money sits in each bank's [[clearing-suspense]] account. The [[payment-lifecycle]] reflects this: a payment moves Accepted → Cleared before it can reach Settled.

There is a second gap on the far side of it. Settlement is [[settlement-finality|final]] when the central bank commits, and each member books its own legs afterwards on being told — see [[unreconciled-position]].`,
  },
  "book-transfer": {
    title: "Book transfer",
    body: `A **book transfer** is a payment between two customers of the *same* bank. Nothing crosses between institutions, so no interbank obligation comes into existence: there is no position for a clearing house to net, no reserves for a settlement agent to move, and no statement that could tell a bank about a book it already holds. The bank recognises the payee as its own and posts both legs itself.

\`\`\`
Alice and Aaron both bank at Bank A:
  Debit  Alice (Liability)   1000   ← the bank owes Alice less
  Credit Aaron (Liability)   1000   ← and owes Aaron more
                             ────
  Net:                          0 ✓
\`\`\`

One posting, one unit of work, and no [[clearing-suspense|suspense]] — suspense holds money that has left one bank and not yet reached another, and this money never left. It joins no clearing cycle and gets no row in \`payments\`: that table carries no \`cycle_id\` precisely because clearing belongs to somebody else, and a transfer that never clears has no business in it.

**Money out is harder than money in.** The payer's account must be active, so a frozen or dormant payer is refused, and the amount is measured against the [[balance-available|available]] balance — which carries the overdraft limit, so a transfer can legitimately push the payer overdrawn. The payee's account is refused only if it is closed: money lands in a frozen one, because that freeze is a debit block, and landing in a dormant one is what revives it. See [[account-status]].

Two refusals are about the instruction rather than about either account: one account named twice, which would post a self-cancelling pair and record that money moved; and two accounts in different currencies, because an amount is one number at one scale and converting is a second operation with a price in it — see [[scheme-asset]].

A payer types the same two things either way: their own account, and the payee's [[account-addressing|address]]. What decides whether it is a transfer or a [[clearing-vs-settlement|clearing]] payment is whether that address resolves in this bank's own register.`,
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

After settlement each bank is *told*, and books for itself: its [[reserve-account|reserve asset]] then changes by exactly its net position, and once its creditor legs are booked too its [[clearing-suspense]] account is back to zero. Neither happens in the central bank's own transaction — see [[unreconciled-position]].`,
  },
  "reserve-account": {
    title: "Reserve at central bank",
    body: `The **reserve at central bank** is each commercial bank's **asset account** representing its claim on the [[central-bank-reserves|central bank]]. It mirrors the bank's reserve liability in the central-bank ledger — the classic **nostro/vostro** reconciliation. It moves when this bank books its own **mirror leg**, from the \`camt.053\` the central bank sends it — which is *after* the cut-off has [[settlement-finality|already settled]].

\`\`\`
Bank A's chart of accounts:
  Reserve at Central Bank (EUR) (Asset) ←── moves at settlement

Central Bank's chart of accounts:
  Reserve: Bank A (EUR) (Liability) ←── the other side
\`\`\`

The two accounts agree **once both institutions have booked**: when Bank A's reserve asset rises by €200, the central bank's Reserve: Bank A (EUR) liability has risen by €200 too. That is the [[double-entry]] invariant applied across two institutions.

**They diverge in between, and that is not an error.** The central bank posts its side first and is [[settlement-finality|final]] there; Bank A is then *told*, by \`camt.053\`, and books its side in a [[unit-of-work|unit of work]] of its own. Until it does, the two balances differ by exactly Bank A's net position. They do NOT always agree, and a divergence is not by itself a bookkeeping error — that would hold only if one institution posted both sides. The window has a name: the [[unreconciled-position|unreconciled position]].

The reserve account is the ultimate destination of all [[net-positions|net settlement]] flows. It is *not* what makes a payment final — the central bank's own commit is — but a bank whose reserve asset has not moved has a cut-off it has not caught up with.`,
  },
  "central-bank-reserves": {
    title: "Central-bank reserves",
    body: `The **central bank** holds one **reserve liability** per **admitted member** **per [[asset]]** — what it owes each member bank, in each kind of money it issues. It opens each one itself, in its own book, when it answers that bank's [[bank-admission|application to join]]; a [[bank-founding|founded]] bank has none. This is the only place where commercial banks actually "meet" and where [[clearing-vs-settlement|settlement]] happens.

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

The central bank's own books stay balanced under [[double-entry]] — one liability falls as another rises. Commercial banks never write into each other's ledgers; they only interact via these central-bank accounts.

The corresponding [[reserve-account|reserve asset]] on each bank's own books moves **afterwards, not in lockstep**: the central bank sends each member a \`camt.053\` and each member books its own side. Lockstep would need the settlement agent to post every member's leg inside its own transaction — a posting in a book that is not its. The gap between the two is real, and it is the [[unreconciled-position|unreconciled position]].`,
  },
  "clearing-suspense": {
    title: "Clearing suspense",
    body: `The **clearing suspense** account is a **[[account-type-liability|liability]] holding in-transit funds** that have left a customer account but have not yet settled between banks. It returns to zero once the bank has booked **both** its halves of a cut-off — which is after [[settlement-finality|settlement]], not at it.

\`\`\`
Timeline for a SEPA Credit Transfer:

1. Acceptance: Alice's funds move into suspense
   Debit  Alice Deposit (Liability)     300  ← she paid
   Credit Clearing Suspense (Liability) 300  ← in transit

2. The bank books the camt.053 it is sent: suspense cleared
   Debit  Clearing Suspense             300  ← transit ends
   Credit Reserve at CB (Asset)         300  ← reserve asset falls
\`\`\`

The suspense balance at any point equals the total value of in-flight payments that have been accepted but not yet settled. If a cycle fails to settle, the suspense remains non-zero — a signal that requires investigation. The [[audit-trail]] records every posting in/out of suspense for reconciliation.

It is also where the [[unreconciled-position|unreconciled position]] shows: settlement is [[settlement-finality|final]] at the central bank, and this account stays non-zero until this bank has booked its own half of the cut-off.`,
  },
  "settlement-finality": {
    title: "Settlement finality",
    body: `**Settlement is final at the central bank**, and the participants catch up afterwards. Once the central bank commits its netting transaction the money has moved between the banks; nothing a member does next unwinds it, including failing to book its own half.

That is not a modelling convenience. In the EU it is the **Settlement Finality Directive** (98/26/EC), whose subject is precisely the moment a transfer order becomes irrevocable — so that one participant's insolvency cannot reach back into a batch that has already settled.

\`\`\`
Clearing house  --pacs.009-->  central bank
Central bank commits its netting transaction   ← FINAL here
  --camt.053-->  each member whose position moved
  --pacs.008 released (via the clearing house)-->  per receiving bank
Each bank posts its own legs, locally, afterwards  ← catching up
\`\`\`

The answer is final either way. A net payer that cannot cover its position is refused **before anything is posted** — \`RJCT\`/\`AM04\`, the same code a debtor's bank sends about a customer's empty account — and the cycle stays Closed with no settlement against it and every payment exactly where the cut-off left it.

The interval between the commit and a member's booking is the [[unreconciled-position|unreconciled position]].

**A [[allows-return|return]] is the same shape, one payment wide.** None of the above is about cut-offs in particular — it is about an institution being final and the members catching up. The central bank reverses the reserves in its own book and is final there, states both members' accounts in a \`camt.053\`, and each bank books its own reserve mirror afterwards. What differs is only the reference the statement carries: a cycle id at a cut-off, a payment id here.`,
  },
  "store-split": {
    title: "One database per institution",
    body: `A deployment of N banks has **N+2 databases**: one per member bank, one for the clearing house, one for the settlement agent. No statement spans two of them. A bank reading another bank's rows finds nothing, and a method reaching for a table its institution's schema does not create is refused by name.

That is not deployment trivia — it decides what every layer above can *say*.

**A payment is three rows, and they legitimately disagree.**

\`\`\`
                payer's bank      payee's bank      clearing house
  legs          the debtor leg    the creditor leg  none — it keeps no book
  cycle         none — a bank has no cycles         the cycle it cleared it in
  status        what it was TOLD  what it was TOLD  what it DECIDED
\`\`\`

The \`ACCP\` announcing an acceptance goes to the bank waiting for an answer to its instruction and to nobody else, so the bank that *answered* the instruction is never told: its copy reads \`Initiated\` until settlement. \`Initiated → Settled\` is a legal edge at a bank and an impossible one at the clearing house. Neither bank is wrong — they were told different things because they asked different things.

**There is no combined [[audit-trail|audit log]] and no order between two of them.** Each institution's \`seq\` counts its own acts, so \`seq 7\` names as many events as there are institutions, and there is no global clock. The honest cross-institution order is *none* — which is exactly the problem a real auditor holding four banks' logs has, and it is answered the same way: with the **messages**, which carry causality that counters do not.

**What it costs is a reconciliation this system did not previously need.** While one database held every book, "a bank's [[reserve-account|reserve]] equals the central bank's liability to it" was true by construction. Now it is a claim about two databases that can disagree, and checking it means holding both at once — which is the one thing no institution in the system may do. The instrument is a **test-level harness** for that reason: it opens all N+2 databases and reports **breaks** (two books that disagree with nothing able to reconcile them) apart from [[unreconciled-position|unreconciled positions]] (a suspense still holding money that something outstanding accounts for). The second is modelled on purpose; only the first is a defect.`,
  },
  "unreconciled-position": {
    title: "Unreconciled position",
    body: `The **unreconciled position** is the interval between the central bank committing a settlement and a member bank booking its own half of it. Settlement is [[settlement-finality|final]] at the commit; the member is only *told* then, and books afterwards in a [[unit-of-work|unit of work]] of its own.

In the books it shows as a non-zero [[clearing-suspense|clearing suspense]]: the money has settled between banks and this bank has not yet cleared the account that held it in transit.

What a bank leaves behind when it *does* book is a **settlement-advice row** of its own. The row and the mirror leg are written in the **same unit of work**, so they commit together or neither does — which means the row says "this bank booked this cut-off", and never "this bank was told and did not". A posting that fails takes the row with it.

\`\`\`
settlement_advices (book_id, reference, asset)
  row present, status = Posted    this bank booked this cut-off
  no row, suspense not zero       the unreconciled position
\`\`\`

So the position is the **absence** of a row against a suspense that has not cleared — and a bank that was told and could not book looks exactly like one that was never told. That is the right trade rather than a gap: booking the leg and recording that you booked it must be atomic, or a bank can post and fail to record.

(A failed posting does NOT leave the row at *Advised*. That status exists in the type, but nothing on the settlement path commits it.)

It is the first payment-layer table **keyed by** book rather than network-wide, because the row belongs to one member: a cycle is the clearing house's and a settlement is the central bank's.

The closing balance the statement carried is stored and **read** — see [[bank-reconciliation|what a bank can catch on its own]] — but it does not tell those two absences apart, and it cannot: a closing balance only ever arrives on a statement the bank holds, so both absences leave the newest one it holds agreeing with its own reserve.

**A [[allows-return|return]] leaves one of these too.** It is settled by the central bank and booked by each bank locally, so the same interval opens for it, and the same absence records it. The row's \`reference\` holds a **payment** id on that path where a cut-off's holds a cycle id, and there is deliberately no column beside it saying which: ids are unique across the store, a member cannot resolve either one, and a reconciliation reads one shape rather than two.`,
  },
  "nostro-reconciliation": {
    title: "Two advices, one balance",
    body: `A bank reconciles **two advices from two institutions against one balance**.

- The **central bank** says what its reserve moved by, in a \`camt.053\` statement of that bank's reserve account. That is the mirror leg against the [[reserve-account|reserve asset]].
- The **clearing house** releases the instructions the cut-off settled, one \`pacs.008\` per receiving bank carrying that bank's share of every file uploaded. Those are the [[creditor-leg|creditor legs]]. The bank that *submitted* gets a \`pacs.002\`/\`ACSC\` per payment instead — the answer to the question it asked, with no leg left to post.

\`\`\`
Bank B, a net receiver, over one cut-off:
  camt.053  (central bank)     Debit  Reserve at CB    → Credit Clearing Suspense
  pacs.008  (clearing house)   Credit payee's deposit  → Debit  Clearing Suspense
  ─────────────────────────────────────────────────────────────
  Clearing suspense back to zero   ✓  only if the two agree
\`\`\`

Its [[clearing-suspense|clearing suspense]] returns to zero only if the two agree, which is the whole point of the split: two **senders** make a check possible. If both advices came from the same institution the bank would be checking a sender against itself and there would be nothing to reconcile.

This is the classic **nostro/vostro** check — the bank's reserve asset against the central bank's reserve liability — with the payment list as a second, independent witness. [[bank-reconciliation|Running it]] is one bank's own act, over one database.`,
  },
  "bank-reconciliation": {
    title: "What a bank can catch on its own",
    body: `A member bank checks its own books against the statements it was sent, over **one database** — its own. It is served on that bank's console alone, and there is nowhere on it to name another bank.

It works because the **[[reserve-account|reserve]] side closes into an identity**. Exactly two things post to a bank's Reserve at Central Bank:

- the **mirror leg** of a \`camt.053\`, which a settlement-advice row names the transaction of;
- a **[[lodgement]]**, which is the bank's own act.

There is no third. So every entry on that account is classifiable from inside, and an entry in neither class is a **break** — something this bank's own books say cannot be true.

| failure | caught by |
|---|---|
| a mirror leg posted at the wrong amount or direction | the advice's closing balance against the reserve's **running** balance at that leg |
| a reserve moved with **no statement behind it** | an entry that is neither a mirror leg nor a lodgement |
| an advice claiming a posting that is not in the book | its transaction absent from the reserve's history |
| a statement **missed, then superseded** by one that arrived | the same running-balance check, at the later statement |
| **the last statement never arriving** | **nothing.** Undetectable from inside |

It is a **walk** and not a comparison of two totals, because the first three have the same total and different histories. And the last row is empty because a closing balance only ever arrives on a statement the bank holds; closing it needs a *periodic* statement, and there is none.

**The [[clearing-suspense|clearing suspense]] has no such identity and cannot be given one**, because the mirror leg is [[netting|netted]]: one cut-off, one figure per member, naming no payment. So the balance cannot be decomposed into the payments that put it there. What a bank can do is **age** it — FIFO over the account's own entries, so a netted leg discharges the batch that opened before it — and report **positions with ages**, never breaks.

A break is a defect; a position is money in flight. Only the first is something to investigate, and a run that called the second a defect would be a run nobody could make against a live network. Those are the same two kinds the cross-institution harness reports (see [[store-split]]); what the narrowing changes is the balance between them — and one thing more: a reserve moved with no statement behind it used to need every institution's database to catch, and one bank now catches it alone.

**Nothing is durable as rows.** There is no findings table — a finding is a pure function of the books at a moment, so a stored one is a cache that can disagree with them. A run leaves a \`reconciliation.run\` [[audit-trail|audit event]] in that bank's own log plus one \`reconciliation.break\` per finding, appended in the **same [[unit-of-work|unit of work]] as the read**. That is what makes it a \`POST\`, and what makes a finding a claim about a consistent snapshot rather than about several reads taken while a cut-off committed underneath them.`,
  },
  "unclaimed-balances": {
    title: "Unclaimed balances",
    body: `**Unclaimed Balances (\<asset\>)** is where a bank puts money that arrives for an account that cannot receive it. It is a **[[account-type-liability|liability]]**, because the bank still owes it — to whoever eventually claims it — exactly as it owes a deposit. Every bank gets one per [[asset]] it operates in, created in its own book when it is [[bank-founding|founded]] — before any scheme has heard of it. It is a [[ledger-vs-subledger|control account]], and it has to be: a pool that could not say *whose* money each part was would be no better than the closed account it stands in for, so every credit here names the address the money was destined for.

The case it exists for: a payee closes their account between their bank's acceptance of the payment and the cut-off. [[account-status|Closed]] is the one status that refuses a credit, and crediting it anyway leaves money no withdrawal can reach and no second close can clear.

\`\`\`
Bank B posts its creditor leg; the payee's account is Closed:
  Debit  Clearing Suspense (Liability)    3000
  Credit Unclaimed Balances (Liability)   3000   ← not the closed account
\`\`\`

The payment still reaches **Settled**, because it did: the reserves moved and the payee's bank has been paid. Whether the **customer** has been paid is a different question, and it is between the bank and its customer.

**Which account it landed in is recorded on the payment**, because a [[allows-return|return]] has to claw the money back from wherever it actually went. Without it a return debits the payee's closed account for money never credited to it, and leaves the unclaimed liability standing. It cannot be worked out afterwards either — an account open at the cut-off and closed later looks exactly like one closed at the cut-off — so the destination is written down when the leg posts.

**Having somewhere for it to go is what makes the check affordable.** With the creditor leg posted inside the settlement agent's one unit of work, refusing a credit would take the whole cut-off down for one retail customer. One payment at one bank fails on its own.

**The same account catches the same case on the [[allows-return|return]] path.** A payer who has closed their account since the payment settled is refunded into their own bank's Unclaimed Balances instead — affordable for the same reason: the return is not one unit of work over three institutions, so the payer's bank has an act of its own to decide in. The diversion happens on a **closed** account and on nothing else: a store failure is not a statement about the account, and money must not be routed on a failure nobody can classify. The mirror case — money the bank has to take *back* and cannot — is [[returns-receivable|Returns Receivable]], and it is an asset rather than a liability.

**Finding the balance is a report; clearing it is a return.** The ageing report decomposes the account into what its balance is made of and how long each part has been there. It is *exact* where the [[bank-reconciliation|clearing suspense's ageing]] is FIFO lots, and the difference is a fact about the postings: every credit here is **one payment's** diverted leg and carries that payment's id, so each lot names the payment, its scheme, and what may now be done about it. The return itself was always available — what was missing was anything that *found* the balance, so an operator had to already know the payment id.

**The window is three days, and it gates nothing.** SEPA gives a receiving bank three *banking business* days to send back a credit it cannot apply. This system has no business date, so it counts **calendar** days and is always early — a balance arriving on a Thursday is called overdue on Sunday where the rulebook would say Tuesday. That costs nothing, because the window is a deadline on the bank's *obligation* and not permission to begin: a return is available for the whole life of a settled payment, and the window only decides which line of the report is printed in bold.

**On a [[scheme-direction-pull|pull]] the bank holding the money is not the bank that may return it.** The balance sits at the **creditor's** bank — the biller closed their account between the collection's answer and the cut-off — and the returner on a collection is the **debtor's** bank, because a \`pacs.004\` on a pull is the payer's bank's instrument and the payer has asked for nothing. What the creditor's bank wants is a *reversal*, \`pacs.007\`, which this system does not have. So those lots are reported and **blocked**, with the reason on them and no deadline: a clock this bank has no message to beat would be a report telling somebody off for something they cannot send.`,
  },
  "returns-receivable": {
    title: "Returns receivable",
    body: `**Returns Receivable (\<asset\>)** is where a bank books a [[allows-return|return]] it has been forced to honour and could not fund out of its customer's account. It is an **[[account-type-asset|asset]]**, and that is the whole point — it is the mirror image of [[unclaimed-balances|Unclaimed Balances]].

| | Unclaimed Balances | Returns Receivable |
|---|---|---|
| Class | [[account-type-liability|Liability]] | [[account-type-asset|Asset]] |
| Direction | the bank **owes** this | the bank **is owed** this |
| Counterparty | a payee it cannot identify | a biller it has identified perfectly well |

Same kind of event — a credit reversed after the bank has already paid out — landing on opposite sides of the balance sheet according to whether the bank knows who owes whom. Every bank gets one per [[asset|asset]] it operates in, created in its own book when it is [[bank-founding|founded]] — before any scheme has heard of it.

It is reached in exactly one case: the clawback is **forced** *and* the biller's account is **closed**.

\`\`\`
Forced clawback; the biller's account is Closed:
  Debit  Returns Receivable (Asset)     3000
  Credit Clearing Suspense (Liability)  3000
\`\`\`

A biller who has *spent* the money but still has an account simply goes overdrawn — the ledger does not refuse a Liability going negative, and an overdrawn biller is a debt the bank collects from a customer it still has. A closed account is the one case with nowhere on the account to put the debit.

**This is the credit risk a creditor's bank takes on when it onboards a biller.** The debtor's eight-week SEPA refund right is unconditional: nobody asks the biller whether it can afford the reversal. So the creditor's bank stands behind its customer's collections whether or not that customer is still solvent — which is why real creditor banks vet their creditors, demand collateral or an indemnity, and price the relationship accordingly. Nothing in this system spreads that risk, so this account is where it accumulates.

Why it exists on **one side and not the other**: a bank can refuse a leg only if it posts it before it sends, and the bank forced to post is the one that hears about the return after it is already final. On a collection that is the creditor's bank. It is not a rule about direct debits — it falls out of the ordering.`,
  },
  "account-addressing": {
    title: "Account addressing",
    body: `An account has exactly one **internal number** — the bank's own key — and a set of **external identifiers**: \`(scheme, value)\` pairs a counterparty actually quotes to pay it. The two are never the same thing; the internal number is never handed outside the bank. \`IBAN\` is the only scheme this system issues today, but the shape is generic — a sort code and number, a routing number and number, a proxy alias, a card PAN are all the same pair with a different scheme name.

**The bank issues the IBAN; it does not accept one.** Opening an account [[address-issuance|mints]] its address, under the country and [[bank-code]] that bank was allocated, and an address offered by whoever asked for the account is refused. That is what makes the bank code inside an IBAN a true statement about who holds the account rather than a claim somebody typed. The set can still be empty — an address can be withdrawn and not reissued — and an account in that state cannot be paid until it has one.

An account can hold several identifiers at once, and gain or lose one without its balance or history moving: that is how a customer keeps an IBAN and later adds a card PAN, or reissues a card. ISO 20022 models the same shape directly — \`CashAccountIdentification\` is a *choice* between \`IBAN\` and a generic \`Othr\` triple — which is why an identifier is a pair rather than a field per kind.

**The [[scheme-asset|scheme]] decides which kind addresses it**, exactly as it decides what it settles in. A leg with no identifier in that scheme is refused, and so is a quoted identifier that isn't one of the account's *in that scheme* — an account with both an IBAN and a card PAN can't have a SEPA transfer accepted against its PAN. Both checks run **per leg**, each by the bank that holds that account, in the same place and for the same reason as the [[scheme-asset|asset check]].

A payment records the address it was reached by whether or not the caller quoted one: the bank that owns a leg fills in that account's single identifier in the scheme's scheme, and refuses to choose when there are several. The submitting bank does this for its own side; the far side's address is back-filled by the *other* bank when the message reaches it — the fill-in runs per leg, at the bank that owns that leg, because the account-owning bank is the authority on how its own account is addressed, not the bank that merely quoted it. That is a claim about the address, and about the payment itself, not about the whole system: see [[counterparty-details]] for what is and is not looked up when a payment is built, and for the two separate questions an address can be put to before one is submitted. The address is a record, not an identity — which is why a [[mandate]] compares its parties by bank and account only. Reissuing the debtor's IBAN would otherwise kill every mandate on the account, permanently.

**An address is compared canonically, not literally — and the difference is a person.** What a register stores and what a \`pacs.008\` carries are the same string, the compact upper-case form the standard calls canonical, so those two can never drift apart. What differs is what a **customer types**: a statement prints an IBAN grouped in fours, a keyboard produces whatever case the typist was in, a form field invites hyphens. All the same address, and compaction cannot be undone, so the lookup normalises **both** sides — separators stripped, case folded. IBAN only: nothing else here has a display form, and stripping punctuation from a card PAN would merge addresses a scheme keeps apart. Nothing stored changes; only the comparison.

**Uniqueness stops at the bank.** A bank's register spans one book — the correct boundary, not a shortcut: a bank-issued identifier is globally unique by construction (an IBAN carries its bank's code, a PAN its BIN) while a proxy alias like a phone number carries no issuer, which is why proxy lookup needs its own central service and this system has none. The register enforces it at write time, within one bank, deliberately with no \`UNIQUE\` constraint behind it: a constraint would fire on the race this read-then-write leaves open, which the register does not, and would fire as a constraint violation rather than as the domain's own refusal. It runs through the same lookup, so it inherits the same comparison rule whole: \`DE20999000010000000001\` and \`DE20 9990 0001 0000 0000 01\` are one address, and the second spelling is refused. That is the point rather than a side effect — an account holding two spellings of one address resolves either way, so no lookup would ever complain about it. Address resolution is what makes that safe **for routing**, and it does so within one bank only: the lookup answers out of the **asking bank's own register**, because a bank that keeps its own database cannot read another's, and no real bank holds a register of another's customers. So it refuses as ambiguous rather than guessing when two of that bank's *own* accounts claim one address.

**Across two banks the collision is refused at the allocation, twice.** Minting cannot reach it — an address carries its issuer's [[bank-code]], so two banks holding one address takes two banks allocated one code. The settlement agent's registry refuses that outright, keyed by country and code, and the clearing house refuses it a second time by declining to publish a code its [[routing-roster|directory]] already holds under a different admission. The second is belt-and-braces and earns its place: the published directory is what every member **copies**, so a duplicate there would make one address ambiguous for the whole scheme, and the clearing house cannot see the registry to check. It is a fault in an allocation rather than in a register, which is why no register can catch it. And it is a claim about routing only — a submission is handed an account number and never resolves, so two accounts sharing an address both stay payable by number. The accounts are distinct and real; what is ambiguous is the address.`,
  },
  "address-issuance": {
    title: "Issuing an address",
    body: `**A bank issues its customers' IBANs. It does not accept one.** Opening an account allocates its address there and then, from the country and [[bank-code]] that bank holds, and an address supplied by the caller is refused outright.

That refusal is the point rather than a side effect. A bank can only mint under the allocation it was given, so it *cannot* issue an address that routes to somebody else — which is what makes the bank code inside an IBAN a fact about who holds the account, and what any directory built on top of it has to be able to rely on.

The account number inside the address comes from a **counter of its own**, not the counter every other identifier in the bank draws on. That one interleaves with transactions and events, so addresses drawn from it would come out \`…0001\`, \`…0007\`, \`…0019\`, with the gaps saying how much unrelated work happened in between. An account number is a thing a customer reads out loud; it is worth a counter. The number is taken inside the same unit of work that opens the account, so an opening that fails does not burn an address.

**Reissuing is one act, not two.** Withdrawing the old address and minting a new one happen together, because there is no longer a call that hands an account a replacement — and because an account between the two would have no address and could not be paid, with nothing recording that it had happened. It moves neither balance, history, nor any [[mandate]] on the account: a mandate authorises debits from an *account*. What it does break is anyone still holding the old address, which is the whole cost of reissuing one and why it is something a bank does deliberately.`,
  },
  "iban-check-digit": {
    title: "The check digits in an IBAN",
    body: `An IBAN carries **check digits**, and their whole job is to reject a typo **offline** — before any lookup, any message, any bank. It is the only refusal in a payment system that needs nothing but the string.

**mod-97-10** (ISO 7064) is the international one, positions 3 and 4. Move the first four characters to the end, map letters to digits (\`A\`=10 … \`Z\`=35), take the whole thing modulo 97; a valid IBAN gives 1. To create one, put \`00\` in the check positions and take 98 − (n mod 97).

Where a country had its own scheme before the international one, that check survives too — they were never retired. **Italy** carries a **CIN** letter, computed from two lookup tables over the 22 characters after it, one for odd positions and one for even. **France** carries a **clé RIB**: 97 − ((89·banque + 15·guichet + 3·compte) mod 97), where letters in the account number map through a table that is **not** IBAN's \`A\`=10…\`Z\`=35. Two different letter-to-digit maps inside one address is what makes the two checks genuinely independent rather than one check computed twice.

**Measured**, over four published example IBANs, exhaustively: **810 of 810** single-digit substitutions caught, and **787 of 787** transpositions of two different digits — at *every* distance, not only neighbouring ones. That is stronger than the property usually quoted and it is not luck: transposing at distance *d* changes the value by a multiple of 10^*d* − 1, and 10 has multiplicative order 96 modulo 97, so no address short of 96 characters can hide one.

And the honest half. Over \`DE89370400440532013000\`, taking every pair of digit positions and every pair of replacement digits: **141 of 15,390 two-character errors go undetected — 0.92%**, close to the 1.03% a uniform remainder would give. That figure is the argument for the national checks and for everything after them: **a check digit says an address was probably typed correctly. It never says the address exists.** Whether anybody holds it is a question only the bank that keeps the register can answer.`,
  },
  "bank-code": {
    title: "The bank code inside an IBAN",
    body: `Every IBAN contains the code of the bank that issued it, and reading it out is the first step of routing a payment anywhere. In Germany it is the **Bankleitzahl**, in Italy the **ABI**, in France the **code banque**.

**It is variable-length and sits at a country-dependent offset**, which is the detail everything else turns on:

| Country | Length | Bank code | National check |
|---|---|---|---|
| \`DE\` | 22 | 8 digits at offset 4 | — |
| \`IT\` | 27 | 5 digits at offset **5** | CIN letter at 4 |
| \`SE\` | 24 | 3 digits at offset 4 | — |
| \`FR\` | 27 | 5 digits at offset 4 | clé RIB at 25 |

Under one country alone the extraction would be four characters at a fixed place, and that shortcut would be *correct*. Across four it is not: Italy's code sits behind a check letter that is not part of it, and Sweden's is three digits. Nothing can pull a bank code out of an address without first knowing which country's structure applies.

**A bank code is allocated, never computed.** A national registry hands one out — the Bundesbank runs Germany's file, the Banca d'Italia Italy's — and the bank is told what it got. Nothing derives one from a BIC, and with numeric codes in every one of these countries nothing could. It is also **never reassigned**, which is what lets anyone hold a copy of the mapping and be at worst incomplete rather than wrong.

The **branch** codes — Italy's CAB, France's guichet — are carried in the address and are deliberately not part of routing. A branch identifies an office within an institution, and routing answers at institution granularity because a BIC does.

**Reading the code out is only half of routing.** Which institution holds it takes a directory of every participant's allocation, and the scheme publishes one for its members to copy. That is what lets a payer type an address and nothing else: SEPA is IBAN-only because every bank subscribes to such a table, not because routing is computable from an address. See [[routing-directory]].`,
  },
  "routing-directory": {
    title: "The directory a bank routes from is a copy",
    body: `A bank code has no computable relationship to a BIC. A bank is \`AURODEFFXXX\` and \`99999999\`, and no arithmetic joins the two — which is exactly why a scheme has to **publish** the pairing rather than let each bank work one out. Three tables carry it, in three institutions, and only one of them is a copy:

| Table | Whose | Answers |
|---|---|---|
| \`bank_codes\` | settlement agent | *who was this code issued to* — the registry's own record of what it gave out |
| \`roster_entries\` | clearing house | *who may be reached, and under which allocation* — the published directory |
| \`routing_directory\` | **each member bank** | *where do I send this* — a snapshot of the row above, pulled by that bank |

The split mirrors the world. The Bundesbank runs the Bankleitzahl file and the EPC keeps the Register of Participants: **who issued this address** is not **may this address be reached**, and they are different institutions answering different questions. A bank with a code and no roster entry is issuing addresses nobody can pay, which is a real state and one this system can hold.

**A member does not ask the clearing house per payment. It subscribes.** It fetches the published directory and replaces its own copy wholesale — a snapshot, because that is what a directory file is, not a delta feed. Between two pulls it routes from whatever it was last given.

So **staleness is real**, and it is the behaviour rather than a defect being tolerated: a bank admitted this morning cannot be paid by a bank that refreshed yesterday. The payer's own bank finds no entry, refuses before either leg posts, and one refresh makes the same payment work. Every real routing directory works this way, which is why being in the scheme's directory and holding a copy of it are two separate things — [[bank-admission|admission]] fills nobody's copy, its own included.

**What makes a copy safe is one invariant: a bank code is never reassigned.** A copy that is behind is therefore *incomplete*, never *wrong*. The failure mode is "I cannot route this yet" and never "I routed it to the wrong bank", and nothing may introduce a path that gives a code back to be issued again.

And the refusal cannot say **which** of two situations it is in — no such bank is in this scheme, or this bank's copy predates it. Those have different remedies, and telling them apart would mean asking the clearing house, which is the lookup the subscription replaces. A refusal claiming to know would be lying about it.

**Not a push, and not a background poller.** A clearing house holding a subscriber list and a retry policy is a delivery system rather than a publisher, and the real vendor does not know who is listening. What the pull does have is a cadence: it is the **first phase of every clearing [[business-day|business day]]**, before anything else moves, so a bank admitted since yesterday can be paid by its neighbours today rather than whenever somebody remembers.

The copy carries a code and a BIC and **nothing else** — no name, because what the published row is written from delivers none, and no list of assets either. Refusing early from data that may be behind would refuse a payment the clearing house would have accepted, so whether a member clears in this currency stays a question for whoever reads the live roster. See [[routing-roster]] and [[counterparty-details]].`,
  },
  "counterparty-details": {
    title: "Counterparty details",
    body: `A payment names two parties, and the submitting bank treats them differently. Its **own** side is overwritten unconditionally — its own BIC, and the account holder's name straight off its own deposit register — because it is the authority on its own customer; nothing is compared, the value the request quoted is simply replaced. The **counterparty's NAME** is not overwritten: it is asserted on the instruction, stored on the payment as the debtor's or the creditor's details, and carried through as given.

**The counterparty's BANK is not on the instruction at all — it is derived.** That element is not a description, it is an **address**: it goes out as \`CdtrAgt\`/\`DbtrAgt\` and the clearing house relays on it without reading anything, so a payer who could type it could choose which bank gets paid. Both failures were measured before the derivation existed: a push whose creditor agent named the payer's own bank came straight back to its sender, and a pull whose debtor agent named the collector had the *collecting* bank post the debit in the payer's bank's book. Neither is expressible now, because there is no field to put a bank in.

**What it is derived from is a table this bank holds.** Not the counterparty's row — that one belongs to a bank this one shares no database with, under the [[store-split|store split]], which is why the derivation was impossible for as long as there was nowhere else to get it. The submitting bank reads the [[bank-code]] out of the counterparty's address and resolves it in its own copy of the scheme's [[routing-directory|routing directory]]. **That is what IBAN-only means**, and it is what SEPA has been since February 2016.

So an address can now fail in three ways, with three different remedies: the instruction names no counterparty at all, and somebody types a name; the address is in a scheme this bank keeps no directory for — a card PAN, a proxy alias, a cross-border transfer — and a BIC is supplied beside it, which is what SEPA itself required before 2016; or the bank code resolves to nothing in this bank's copy, and the remedy is a refresh, or the payee's bank is not in this scheme and never will be. **The payer supplies the counterparty's address and its name, and nothing else; its own bank supplies everything about itself and derives the rest from the address.**

**A [[mandate]] derives its debtor's bank once, when it is signed, and never again.** A mandate authorises debits from an account at the bank the debtor signed up against; an authorisation that silently followed a directory to a different institution is a behaviour no real scheme has.

**The name is refused where it is missing and unchecked where it arrives.** The SUBMITTING bank refuses an unnamed counterparty outright, before either leg posts, so nothing is half-done. The RECEIVING bank checks neither field. For its own side — the creditor's account on a push, the debtor's on a pull — it reads that account anyway, and could in principle compare the name it finds there against what the other bank asserted. It does not, and that is deliberate restraint rather than inability: overwriting the name would leave the payment it stores saying something different from the message that already went out on the wire.

**"Resolve this address" is two questions with two answers, and neither of them is a name.** *Which account holds it* is answered out of one bank's **own register** — so a bank can tell its own customer about its own accounts, and nothing whatever about anybody else's. *Which institution does it reach* is answered from the copy of the published directory, and what comes back is a BIC, because the roster was never told a name either. So a send form can show which bank an address routes to and can never show whose account it is, and no name a payment carries is ever the product of one bank reading another's register. The name travels on the instruction because there is nowhere else a payment may take it from. Confirming it against the payee's own bank is a different question with its own message pair behind it, and this scheme does not ask it.`,
  },
  "audit-trail": {
    title: "Audit trail",
    body: `The **audit trail** is an **immutable, append-only log** of every mutation in the system. Nothing is ever deleted. Every event carries a unique ID, timestamp, event type, and the full payload of the affected entity.

Events recorded, grouped by the layer (**scope**) that produced them:

- **ledger** — ledger/subledger/account creation, transaction posting, [[reversal]]
- **deposit** — account opened/frozen/closed, [[holds|hold]] creation, [[hold-release|hold release]], [[hold-capture|hold capture]], [[snapshot|end-of-day snapshot]]
- **payment** — the four acts of an admission, one at each institution that performs it (founded and membership recorded at the joining bank, the settlement account at the central bank, the routing entry at the clearing house), [[mandate]] created/revoked at the *creditor's* bank, every payment as each institution advances its own copy, [[clearing-vs-settlement|clearing cycles]] at the clearing house and settlements at the central bank

Each event is written **inside the transaction of the operation it describes**, so an operation that rolls back leaves no record claiming it happened. The log is unbounded, so the API pages it: **limit** (default 100, max 1000) and **before**, an exclusive cursor on the event's sequence number.

**There is one log per institution, not one log.** Since the [[store-split|store split]] each institution keeps its own, and \`seq\` counts that institution's own acts — so \`seq 7\` names as many events as there are institutions, and a cursor taken from one console means nothing on another. A payment's history is spread across the three institutions that touched it, each recording what *it* did, and there is no order between two of them. See [[store-split]].

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

**What actually happens today:** a snapshot is written, and read back only by asking for it — one day's, or a range of them. Interest accrual reads the value-dated entry list fresh on every run and never consults one; no balance query consults one either. What is captured today is the raw material a checkpoint would read from.

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
    body: `A deposit account has **no ledger of its own**, and no line of its own in the chart of accounts either. Its statement is *derived* — every [[double-entry]] transaction that touches this customer's **position**, meaning the customer-deposit [[ledger-vs-subledger|control account]] filtered to them as holder, projected onto that one leg, oldest→newest, with a running balance.

The running balance reconciles to the account's **book** balance: a built-in correctness check. Holds never appear here — they post nothing to the ledger until captured.`,
  },
  "statement-amount": {
    title: "Why credits add",
    body: `Your deposit is a **liability** to the bank — money it owes you. Its [[normal-balance]] is credit, so a **Credit increases** your balance (shown \`+\`) and a **Debit decreases** it (shown \`−\`).

Expand a row to see the full balanced transaction: your line is one leg; the contra account is where the money came from or went to — often the [[clearing-suspense]] account while a payment is in flight.`,
  },
  "relational-mapping": {
    title: "The ledger as relational tables",
    body: `The whole accounting model is seven tables, and the shape of the [[double-entry]] rule is visible in them: a transaction is a **parent row** and its legs are **child rows**, so "a posting has two or more balanced entries" is a one-to-many relationship rather than two columns.

\`\`\`
ledgers ─┬─▶ subledgers ─┬─▶ accounts ◀─ slot_accounts
         │               │      accounts.control  — one line for many?
transactions ─▶ entries ─┘   entries.subsidiary_id — whose money this leg is
\`\`\`

Two details are easy to get wrong. \`entries\` needs an explicit **position** column, because a transaction's entries are ordered and a table has no order. And listings are ordered by \`(created_at, seq)\`, never by id — IDs are counter-derived strings, so \`dep_10\` sorts before \`dep_8\` and a customer list would silently reorder itself the first time a counter crossed a power of ten.

A customer's deposit account is **not** a row in \`accounts\`, and \`deposit_accounts\` carries no column pointing into the chart of accounts at all — nor do \`facilities\`. Their money is a value in \`entries.subsidiary_id\` under a [[ledger-vs-subledger|control account]], and one customer's balance is that account's sum with \`AND subsidiary_id = ?\` added. \`slot_accounts\`, keyed by \`(product, slot, asset)\`, is what says which account a given flow posts to — the mapping is a row rather than a name matched in code.

What is *not* a column is the balance — see [[derived-balance]]. What is not a single column is the primary key — see [[book-scoped-id]].`,
  },
  "derived-balance": {
    title: "A balance is an aggregate, not a column",
    body: `There is no \`balance\` column anywhere. A book balance is computed on demand by summing the account's entries, signed by [[normal-balance]] — so the account's *normal* direction is a parameter of the query, not a constant in it:

\`\`\`sql
SELECT COALESCE(SUM(CASE WHEN direction = ? THEN amount ELSE -amount END), 0)
FROM entries WHERE book_id = ? AND account_id = ?;   -- the first ? is the normal direction
\`\`\`

Hardcoding \`debit\` there would be right for every asset and expense account and would negate every liability, equity and revenue one: a customer's checking account holding 750.00 would report −750.00.

One customer's balance is the same query with \`AND subsidiary_id = ?\` added, because their account is not a line of the chart of accounts but one holder under a [[ledger-vs-subledger|control account]]. The pooled total is that clause dropped — so the control figure and the detail behind it are one sum asked two ways, never two numbers that could disagree.

A stored balance is a **cache of a derivable fact**, and caches go stale: any bug, crash or concurrent write that updates one of the two without the other leaves a number no one can reconcile. Deriving it means the [[audit-trail|append-only history]] is the single source of truth and the balance cannot disagree with it.

The cost is that reading a balance is an aggregate over every entry ever posted. The standard answer is not to add the column back but to checkpoint: a query would start from the nearest checkpoint and replay only what came after. The [[snapshot|end-of-day snapshot]] records a day's figure so that it *can* — but that is the design, not the current behaviour. No balance query in this system consults a snapshot; every one still sums the whole entry list.

Note also that a reversal is a *new, opposite* posting ([[reversal]]), so the sum still includes both — which is why the balance of a reversed transaction's account nets out rather than the row disappearing.`,
  },
  "unit-of-work": {
    title: "Unit of work",
    body: `A **unit of work** is one atomic scope — \`BEGIN\`, do everything, \`COMMIT\`, or \`ROLLBACK\` and it is as if nothing happened. Every mutation in this system runs inside one, and the [[audit-trail|audit event]] is written inside the *same* one, so a rolled-back operation leaves no record claiming it happened.

It has to span all three layers, because the operations do. A payee's bank paying its own customer posts a [[creditor-leg]] in the ledger, moves a deposit balance, and marks the payment settled — a partial success there would credit the customer against a payment the system still calls unpaid.

\`\`\`
SettleAtBank, at the payee's own bank:
  BEGIN
    creditor leg in this bank's book          (ledger layer)
    deposit balance follows                   (deposit layer)
    payment marked Settled                    (payment layer)
    audit event appended
  COMMIT   ← all of it, or none of it
\`\`\`

What a unit of work may **not** span is more than one institution — and since the [[store-split|store split]] that is a fact about the database rather than a discipline the code keeps. A unit of work is one *database's*, and each institution has its own, so there is no transaction that could hold two.

Settling a [[clearing-vs-settlement|clearing cycle]] is the central bank's own scope for the reserves, plus one of that member's own for every leg a member books afterwards — joined by messages, with the interval between them a real [[unreconciled-position|unreconciled position]] rather than something a transaction can hide. A [[allows-return|return]] is the same rule applied to one payment: the returning bank's leg, the reserve reversal and the other bank's leg are three scopes at three institutions.

Nesting one unit of work inside another is refused rather than allowed: the inner scope would be a *separate* transaction that commits even when the outer one rolls back. So every operation either opens a unit of work or joins one already open, and never both.`,
  },
  "row-locking": {
    title: "Locking a row before you decide on it",
    body: `Checking a balance and then posting against it is two steps, and between them another transaction can post too — so both see enough money and together they overdraw the account. The check has to be tied to the write that depends on it, and there are two ways to do that.

**Prevent it.** Postgres takes the lock at the read:

\`\`\`sql
SELECT id FROM accounts
 WHERE book_id = $1 AND id = ANY($2)
 ORDER BY id
   FOR UPDATE;     -- held until COMMIT
\`\`\`

\`ORDER BY id\` is not cosmetic. Two transactions touching overlapping accounts in *different* orders would each hold a row the other wants — a deadlock. Taking locks in one agreed order means the second one simply waits.

**Or detect it.** SQLite admits one writer at a time and *refuses* a transaction that read at one snapshot and then tries to write after somebody else has committed. There is nothing to lock and nothing to order: the loser is turned away, the whole [[unit-of-work|unit of work]] is run again with fresh reads, and the second withdrawal now sees the balance the first one left and is refused by the domain's own rule rather than by the database's. That is what this system does — the lock method exists in the store interface and has no statement behind it.

Both answers exist because the hazard does. A single process-wide mutex was hiding it for free in the in-memory store, which is why it only becomes visible when the state moves into a database. See [[idempotency-race]] for the second race it was hiding.`,
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
 WHERE book_id = ? AND id = ? AND status = 'Posted';
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

A single-column key would force globally unique numbering and destroy the [[ledger-vs-subledger|chart of accounts]] as a readable, per-bank structure.

The composite key survives even though the [[store-split|split]] has made it redundant, and that is worth knowing. A bank's database now holds exactly one book — its own — so \`book_id\` takes one value in it, and a query that forgot to scope by it could no longer return another bank's rows: there are no other bank's rows to return. It stays because a book id is a real key rather than a convention, and because dropping it from thirty tables would buy nothing.

**There is no network-wide book.** The payment layer's own entities — participants, payments, mandates, cycles, settlements — each have exactly one owner: a bank's own record of itself and its [[mandate|mandates]] at that bank, a [[net-positions|cycle]] at the clearing house, a settlement at the central bank. Each is keyed and sequenced under its owner's book, in its owner's database, and a payment is [[store-split|three rows in three of them]].`,
  },
  asset: {
    title: "Asset (what an account is denominated in)",
    body: `An **asset** here is a unit of value that accounts are counted in — EUR, USD, BTC. It carries a \`code\`, a \`name\`, a [[asset-scale|scale]] and a class (\`Fiat\` or \`Crypto\`).

> **Not the same "asset" as [[account-type-asset|the account type]].** An account's *type* says whether it is something the bank owns or owes; its *asset* says what kind of money it is counted in. A euro deposit and a bitcoin deposit are both **Liability** accounts.

Three properties do the work:

- **Defined in code, not stored.** The known assets are a list in code, not rows in a table — an asset *definition* is a fact about the world, and "BTC has 8 decimals" is true in every book. Two books able to disagree about it would be a bug, not a feature. Same reasoning as [[scheme-asset|a payment scheme being defined in code]] rather than as a row.
- **One asset per account, fixed at creation.** An account number and its currency are inseparable in real banking, which is why IBANs are per-currency.
- **So a balance stays a scalar.** If one account could hold several assets, every balance, statement line and snapshot would carry a *map* instead of a number. Binding the asset to the account pushes that dimension into the chart of accounts, where it costs one more account rather than one more parameter on everything.

A bank in three currencies therefore has three cash accounts, not one holding three kinds of money. An account naming a code the system does not know is refused.

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

So the check sums debits and credits *within* each asset and requires each to net to zero. A failure names the asset that broke, not merely that the transaction did.

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
    body: `Every [[payment-lifecycle|payment scheme]] names the one [[asset]] it carries. \`SCT\` and \`SDD\` both return **EUR** — SEPA is the *Single Euro Payments Area*, and a "SEPA dollar transfer" is not a thing. A scheme in another currency is another scheme, with its own rulebook and its own cycles. A scheme makes the same kind of declaration about *addressing*: see [[account-addressing]] for that one, the sibling of this.

Each account is checked against it **by its own bank**; a mismatch is refused. The check sits in the debtor's and the creditor's halves rather than inside a scheme's own validation, so it runs for every scheme — a future card scheme that places a hold instead of checking funds would otherwise skip it silently.

It is two comparisons and not one, and that is worth stating rather than hiding. One comparison needs both accounts read inside one function, at a moment both ends are in view, and the message layer has no such moment: no actor sees both accounts, so each bank compares its own. That is strictly *weaker*, and strictly what a real bank can do.

**The ledger cannot catch this when the debtor leg posts**, which is the part worth understanding. A payment is never one posting: the [[debtor-leg]] is a transaction in the payer's bank's book; the [[creditor-leg]] is a separate one in the payee's, written later at [[clearing-vs-settlement|settlement]]. Give Alice a euro account and Bob a bitcoin one, and the payer's bank writes exactly this:

\`\`\`
Bank A:  Debit  Alice EUR     3000   ← balances in EUR ✓
         Credit Suspense EUR  3000
\`\`\`

Impeccable [[double-entry]]. Nothing in it contains the claim that a posting in another bank's book is its other half — and no ledger can see a claim that is not in front of it. No bank here holds both ends at once, which is what makes the ledger's catch late rather than absent.

**It is caught at the [[creditor-leg|creditor leg]], which is the argument *for* the check.** That leg is built from the creditor's suspense account *in the scheme's asset*, so what actually gets posted is a EUR debit against a BTC credit:

\`\`\`
Bank B:  Debit  Suspense EUR  3000   ← EUR
         Credit Bob BTC       3000   ← BTC — does not balance
\`\`\`

That is the ledger refusing a transaction that does not [[per-asset-balance|balance per asset]]. But it arrives **hours after the payer was debited**, with the [[net-positions|cycle]] already settled and the reserves already moved, and it names an imbalance rather than the payment behind it. The scheme's asset check turns a late, misattributed failure into an immediate, correctly attributed one.

The general rule: an invariant is enforceable where the whole of it is visible, and cheapest where it is visible earliest.`,
  },
  "participant-assets": {
    title: "Internal accounts, one set per asset",
    body: `A bank's internal accounts — [[clearing-suspense|clearing suspense]], [[reserve-account|reserve at the central bank]], [[unclaimed-balances|unclaimed balances]], [[returns-receivable|returns receivable]], [[vault-cash|vault cash]], and its [[settlement-account|settlement account]] in the central bank's own book — exist **once per [[asset]] it operates in**.

A bank clearing both a euro scheme and a dollar one holds two suspense accounts and two reserve accounts, not two currencies inside one. Partly because [[asset|an account is bound to a single asset]], and partly because [[net-positions|netting]] a euro position against a dollar one does not produce a smaller number, it produces a meaningless one.

\`\`\`
Bank A
├── EUR: suspense, reserve, unclaimed,
│        returns receivable, vault cash,
│        settlement
└── USD: suspense, reserve, unclaimed,
         returns receivable, vault cash,
         settlement
\`\`\`

They are a child row keyed \`(bank, asset)\` rather than a column apiece on the bank, which also makes adding a *kind* of account cheap: returns receivable joined the row when the return path needed somewhere to book a forced clawback, and vault cash joined it when taking cash in stopped being the same act as putting it on reserve. One column there is one account per asset automatically.

All but one of them are created in the bank's own book when it is [[bank-founding|founded]]. The settlement account is the exception: the central bank opens that one, in its own book, when it answers the bank's application — so on a founded bank the column is empty.

[[clearing-vs-settlement|Settlement]] resolves the set from the **cycle's** asset — which comes from the cycle's [[scheme-asset|scheme]] — once for the whole batch. A member holding a net position but no accounts in that asset fails the entire settlement before anything posts, exactly as an underfunded member does.

There is deliberately **no fallback** to a default asset. Defaulting to euro would settle a dollar cycle in the wrong money, quietly, in the one place in the system where money becomes final.`,
  },
  "vault-cash": {
    title: "Vault cash is the bank's own money",
    body: `**Vault cash** is the notes in the drawer: an [[account-type-asset|asset]], in the bank's own book, and the only account on a bank's chart that is **nobody else's promise**.

Compare it with the others and the point is immediate:

\`\`\`
Reserve at Central Bank  a claim ON the central bank
Returns Receivable       a claim ON a biller
Clearing Suspense        money OWED to a counterparty's customer
Unclaimed Balances       money OWED to someone unidentified
Vault Cash               money. Held.
\`\`\`

**It is where cash paid in over the counter lands.** A customer hands over €100; the bank's vault cash rises by €100 and what it [[account-type-liability|owes]] that customer rises by €100. One institution, one pair of entries, one book — and nobody else has to agree, which is why taking a deposit sends no message anywhere.

\`\`\`
Debit  Vault Cash (EUR)      100  (Asset ↑)
Credit Alice's deposit (EUR) 100  (Liability ↑)
\`\`\`

Every [[bank-founding|founded]] bank has one, per asset, from the moment it is founded — **before** any scheme has heard of it and whether or not one ever does. That is the shape of "a bank's counter has nothing to do with its central bank account": taking cash in is one institution's act, and it needs nothing from anybody. (In this system a founded bank has no customer to take it in *for*, because addressing an account needs a [[bank-code|code]] a registry allocates. The refusal is about the address, not about the counter, and the two are worth keeping apart.)

**A bank cannot settle out of it.** Interbank obligations are discharged in [[central-bank-reserves|central-bank money]], not in the bank's own cash, so a bank that takes deposits and never [[lodgement|lodges]] accumulates vault cash it cannot pay anyone with. So the balance here is a real figure and not a way-station: it is **how much of what this bank's customers paid in has not been placed on reserve**.

A deposit that debited the [[reserve-account|reserve]] directly and posted the matching credit in the central bank's own ledger would be one bank writing in another institution's book — which is the thing that cannot survive each entity owning its own database.`,
  },
  "lodgement": {
    title: "A lodgement is a conversation; a deposit is not",
    body: `A **lodgement** is a bank swapping [[vault-cash|vault cash]] for a claim on its central bank. It is how [[central-bank-reserves|reserves]] — which start at zero — actually come to exist.

**The contrast with a deposit is the whole idea.** A customer paying cash in is *one* institution's act: the bank takes the notes and writes two lines in its own book. Nobody else's book moves, nobody has to agree, and there is nothing to tell anyone.

Moving that cash onto reserve is a **different act between two institutions**. The bank cannot write in the central bank's book, so it cannot credit its own reserve account — only the account servicer can. And the way one institution asks another to post is a **message**:

\`\`\`
bank  --camt.050-->  central bank      "credit my reserve, here is the cash"
bank  <--camt.025--  central bank      "done" (or "no")

bank's book:          Debit Reserve at Central Bank / Credit Vault Cash
central bank's book:  Debit Settlement Assets       / Credit Reserve: <bank>
\`\`\`

**Two books, two [[unit-of-work|units of work]], one message each way.** That is not machinery added for realism — it is the only shape available once each institution keeps its own books. A single transaction spanning both would be the very thing the split exists to remove.

The bank posts its own leg **before** it sends, and the reason is the message rather than the money: a \`camt.025\` carries no amount, so a bank cannot work out what to post from the answer. Between the send and the receipt its reserve mirror says more than the central bank's book does — the same [[unreconciled-position|unreconciled position]] a cut-off opens.

A bank with no [[settlement-account|reserve account]] to lodge into is refused. That check belongs here and not on taking a deposit. See [[bank-founding]].

\`camt.050\` is closer to the real thing than most of this system's messages: it is exactly what a TARGET2 or CLM participant sends to move liquidity onto its RTGS account.`,
  },
  "bank-founding": {
    title: "A bank exists before a scheme answers it",
    body: `**Founding a bank and giving it a place in a scheme are different acts, at different institutions.** Founding is the bank's own: it gets a book, a chart of accounts, its [[participant-assets|per-asset plumbing accounts]] and a deposit product to sell. What comes out is a working bank that is in no scheme.

Its own book is unrestricted — it publishes products, adds ledgers, opens general-ledger accounts. What it cannot do is anything that needs something another institution gives out:

\`\`\`
Before a scheme has answered it
  book, chart of accounts, product
  a vault cash account, per asset
  CANNOT open a customer account   no code to address one under
  CANNOT lodge cash on reserve     no reserve account to lodge into
  CANNOT pay, CANNOT be paid

After — the above, plus
  a bank code, allocated by a registry
  a settlement account at the central bank
  a row in the scheme's published directory
  opens addressable accounts, lodges, settles
\`\`\`

**The sharpest one is that it cannot give a customer an ADDRESS.** Every account is opened with an IBAN minted under the country and [[bank-code|bank code]] its bank was allocated, and that code is allocated by a registry when the application is answered — so a bank no registry has answered has no address range and can open no customer account at all. It applies to a market; it is *given* a code. A caller allowed to supply one would make the whole [[routing-directory|routing directory]] unnecessary, and would be wrong about the world.

**Taking money in is the one people guess wrong.** The guess is that such a bank cannot be funded — that cash paid in raises the bank's reserve in the same [[unit-of-work|unit of work]], so there is no reserve to raise until a settlement agent has opened one, and the refusal names the membership.

That is a false statement about banking. **A bank's counter has nothing to do with its central bank account.** A bank that has founded itself and joined no scheme can open its doors and take cash; it holds what it takes as [[vault-cash|vault cash]], which is its own money in its own hands, and nobody else's book moves. What stops this one is the address, not the counter — a different refusal, about a different act, and worth keeping apart from the one below.

What it cannot do is turn cash into reserves. That is a [[lodgement]] — a request to the central bank, because only the central bank can credit an account in the central bank's book — and it is refused for a bank that has no reserve account to lodge into. **That is the act the reserve refusal is true about.**

**Paying is a refusal, not an inability**, and it is refused of BOTH parties in two different places. *Being paid* is refused by the **payer's own bank**: a bank in no published directory is in nobody's copy of one, so an address under its code resolves to nothing and the payment dies before either leg posts. *Paying* is refused at the network's door, which asks the directory whether the submitting bank is a member, and again at the clearing house before a payment is taken into a cut-off. Nothing else would stop it — an [[overdraft|arranged overdraft]] gives a customer spendable money with no deposit at all — and what the refusals cost when they did not exist is the point: the cut-off cannot name a non-member in the settlement instruction, so one such payment stopped the **whole cycle**, with every other member's payments in it.

**Admission is not one transaction.** One write covering the bank's accounts, the central bank's and the clearing house's together would mean "a bank can never exist without the accounts it needs", and no real admission has that guarantee. A bank is licensed and built long before any scheme has heard of it, and no institution may write in another's book to give it what it lacks. See [[bank-admission]].`,
  },
  "settlement-account": {
    title: "The settlement account is the central bank's",
    body: `A bank's **settlement account** is opened **by the central bank, in the central bank's own book**. It is \`Reserve: <Bank> (<asset>)\` — a [[account-type-liability|liability]] of the central bank's, numbered in the central bank's chart of accounts. The bank does not hold it.

What the bank holds is its **number**, the way an account holder knows their IBAN without holding the bank's ledger. Two records, two owners:

\`\`\`
The central bank's row
  SettlementMember, keyed by BIC
  one account id per asset,
  allocated in its own book

The bank's own row
  BankAccounts.Settlement
  the number it was told, quoted
  whenever it [[lodgement|lodges]] cash
\`\`\`

One account **per asset**, because a reserve in euro says nothing about a reserve in dollars. That is also why a bank joining in two assets applies twice: the account-opening request carries one currency, so it asks once per asset and is answered once per asset.

The central bank's row is keyed by **BIC** and by nothing else — a settlement agent holds no roster and is told no bank ids, so the only identifier it is ever told is the one on the message. It knows which account it holds for whom, and nothing about how the member runs: not its book, not its subledgers, not its product.

It does keep a **second register**, and it is a different kind of thing: the [[bank-code|bank codes]] it has allocated, one country at a time, which is the national registry's job rather than the account servicer's. Two registers because they answer two questions — *whose account do I hold* and *who was this code issued to* — and this system runs both out of one institution, which four national registries do in the world.`,
  },
  "routing-roster": {
    title: "The routing directory says who may be addressed",
    body: `The scheme's **routing directory** belongs to the clearing house, and it answers one question: **where do I send a message addressed to this member?** It is not a register of banks. A bank absent from it exists perfectly well — it is simply not somewhere this scheme will send anything.

Each entry carries a **BIC**, the country and [[bank-code|bank code]] that member issues its customers' addresses under, the assets it clears in, and the reference of the admission that put it there. The allocation is what makes it a routing *directory* rather than a guest list: it is the pairing every member [[routing-directory|copies and routes from]], and the clearing house learns it for free, off the same answer the row is written from.

What the entry does *not* carry is the point:

- **no account of any kind** — no account id, no subledger, no product, no book. A clearing house holding one would be holding the means to reach into a bank's ledger. The row this replaced carried the central bank's account ids, and readers in three institutions resolved their postings through it.
- **no name** — what the row is written from identifies the account owner by BIC and delivers no legal name at all. Routing is an address; a name here could only be the clearing house remembering something nobody told it. That absence travels all the way down to a payer, who resolves an address and is shown a BIC.

It is keyed by BIC because a clearing house routes what a message addresses, and a message addresses a BIC. See [[bank-admission]] for how the row comes to be written, and by whom.`,
  },
  "bank-admission": {
    title: "Admission is four acts, in order",
    body: `Admitting a bank to a scheme takes **three institutions**, and none of them can do another's part. The order is the content: **the settlement account is opened first and the routing entry is written second**, because a scheme will not route to a member that cannot settle.

\`\`\`
1  the bank            founds itself
2  the central bank    allocates a bank code, opens
                       one settlement account per asset
3  the clearing house  writes the routing entry
4  the bank            records what it has been told
\`\`\`

1. The **bank** founds itself first — see [[bank-founding]] — and asks for a place in a scheme, one request per [[asset]], all quoting one reference so the scheme can tell they are one admission. It names the **country** whose registry it wants a bank code from, and it brings no code of its own.
2. The **central bank** does two things in one unit of work, out of two registers: it **allocates a [[bank-code|bank code]]** in the country the request named, and it opens one [[settlement-account|settlement account]] per asset in its own book.
3. The **clearing house** writes its [[routing-roster|routing entry]] from what the settlement agent opened rather than from the bank's own word, so a bank the scheme routes to is one it can already settle for. It refuses a code its directory already holds under a different admission — a second refusal for a different reason: it cannot see the registry, and its own directory is what every member copies.
4. The **bank** records the account numbers and the code it has been allocated, and can from that moment open addressable customer accounts.

**Four units of work, not one**, and each is one institution writing in its own book. Nothing carries between them and nothing puts them on a wire: which banks a network has is settled when it is set up, not asked for at runtime. A failure part-way leaves a bank with rows at some institutions and not others — something to retry and something found by holding all three books against each other, which no institution in the scheme may do — see [[nostro-reconciliation]] for the same shape of comparison one institution can make.

**What admission does not do is fill anybody's [[routing-directory|routing directory]]**, its own included. Being in the published directory makes a bank reachable; holding a copy of it is what makes a bank able to reach anyone, and each member pulls its own copy on its own account. A bank admitted this morning is unpayable by every member that refreshed yesterday, and cannot itself pay anyone until it refreshes.

**Why none of this is a message.** Scheme membership is *contractual* — an adherence agreement, signed — and travels on no message at all. And a real central bank does not open an RTGS account over a payment network either: it is reference data (in TARGET, CRDM static data and \`reda\`), set up by the central bank's own operations. What is real here is the **sequence and the ownership** — who may open which account, in whose book, and in what order.`,
  },
  "credit-facility": {
    title: "Credit facility",
    body: `A **credit facility** is a bank's extension of credit to a customer, tracked outside the demand-deposit layer: a [[term-loan|term loan]] (a fixed principal, disbursed once and repaid on a fixed schedule) or a [[revolving-line|revolving line]] (a reusable limit the customer draws down and repays repeatedly, billed in cycles).

An [[overdraft|overdraft limit]] extending a deposit account below zero is a THIRD form of credit in this system, but it is deliberately not a facility: it has no drawn principal of its own, no schedule and no commitment — it is priced credit layered onto an existing liability position, not a standalone [[account-type-asset|asset]]. (It does get a position under the bank's accrued-interest receivable the moment a non-zero rate is set: interest earned is a real asset wherever it was earned.)

A facility is not a line of the chart of accounts either. It is one **holder** under three of the bank's [[ledger-vs-subledger|control accounts]] — drawn principal and accrued interest receivable, both [[account-type-asset|Asset]], plus the Liability line for interest the bank owes back after a backdated correction — with its own id named on every entry that belongs to it. A bank lending to ten thousand borrowers has three chart-of-accounts rows for its loan book, not thirty thousand.

The **commitment** is the ceiling the customer may draw against, and it is stored: it is a fact about the contract. What is **drawn** is DERIVED — the loan-principal line's balance filtered to this facility, never a stored field, the same discipline [[derived-balance|a book balance]] follows — and the **accrued interest** is the rounded minor-unit figure of the facility's own exact [[accrued-interest|accrued-interest record]], which its position in the receivable always equals.`,
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

[[accrued-interest|Accrued interest]] is tracked at higher precision than the ledger posts (rounding to a whole minor unit every day would quietly lose a fraction of it); the account's accrued figure is the rounded amount the general ledger actually holds. It is charged to the account — [[capitalization|capitalized]] into the debit balance — on a billing cycle, which is why an overdraft sitting at its limit is not costless the way a \`0\`-rate account is.

The rate an account pays is usually its [[product-catalogue|product]]'s rather than its own — it [[pinned-vs-floating|floats]], so one published version reprices a whole book — unless the customer has a negotiated [[pricing-overlay|overlay]]. Both the limit and the rates are [[effective-dated-terms|effective-dated]], and every night's accrual is a RECOMPUTATION of the whole account rather than an increment. That is what lets it reach all the way back to account opening: every day is re-derived at the terms that were actually in force on it, so a back-dated posting is trued up wherever it lands, including before a repricing — which a moving window would silently stop at.`,
  },
  "effective-dated-terms": {
    title: "Effective-dated terms",
    body: `A product's terms are a **timeline**, not a set of columns: one row per repricing, each carrying the day it takes effect, and a repricing appends a row rather than editing what an earlier day already said.

The reason is that every interest figure is a function of three things — account state, event history, and configuration. The first two are immutable and replayable here. If the third could be edited in place, that whole investment is undone, because "what did this account's product say on 15 July?" stops having a stable answer.

There is a sharper consequence than an audit weakness. While terms were mutable, the [[interest-accrual|accrual]] recompute could only reach back to the last repricing — reaching further would re-derive old days at *today's* rate. A repricing closed the old window out and opened a new one at itself, so a [[booking-date|back-dated]] posting landing behind it was trued up only from the repricing forward, while the days between where it took effect and the repricing kept the interest computed without it, permanently — the repricing was a line the correction stopped at. With a timeline, every day is re-derived at the terms actually in force on it, so the window opens at account inception and the correction always lands.

Each row carries **two** dates, and the pair is the [[booking-date|booking-date/value-date]] distinction applied to configuration: when the repricing was *entered*, and when it takes *economic effect*. They can differ in either direction — a rate agreed on the 1st and keyed in on the 15th is backdated; a rate agreed for next month is future-dated and, for a product with no instalment schedule to diverge from — an overdraft or a revolving line — simply sits inert until the end-of-day runs reach it.`,
  },
  "product-catalogue": {
    title: "Product catalogue",
    body: `A **product** is a named catalogue entry an account is opened *from* — "Basic Current Account" — and its price is an [[effective-dated-terms|effective-dated]] timeline of immutable published **versions**, one row per repricing.

The point is that a price becomes a THING with a name and a publication event, rather than a number copied onto each account. Repricing a book of ten thousand accounts is then one published row, not ten thousand writes with no shared cause — and afterwards there is an artefact naming the decision, instead of ten thousand coincidences.

A version is a **draft** until it is published: a draft prices nothing, so the published version before it stays in force through its day. That is what stops "immutable" from meaning a typo in a rate is permanent. Publishing freezes the row and stamps a content hash which is **verified every time a day is priced**, so a row edited directly in the database stops the accrual rather than quietly pricing a day nobody published.

Publication is **forward-only**: a version effective before today is refused. It would move interest already charged on every account bound to the product at once, and the audit log would be the only control on it. Retroactive repricing stays where its blast radius is one named customer — the [[pricing-overlay|pricing overlay]]. This is deliberately less capable than a four-eyes maker-checker regime; forward-only is the mitigation this system ships instead.

**Retiring** a product takes it off sale without unpricing anything: the accounts already sold from it keep resolving against its versions for as long as they live. A bank that could not express that would have to keep dead products on sale.`,
  },
  "pinned-vs-floating": {
    title: "Pinned and floating terms",
    body: `An account's overdraft terms come from two places at once, and which parameter comes from which is the whole design:

- The **rate** *floats* with the [[product-catalogue|product]]. One published version reprices every account bound to it, per day, with no write to any account.
- The [[overdraft|limit]] is *pinned* to the account. It never comes from the catalogue.

The reason is that they are different kinds of decision. A rate is a **price the bank publishes**; a limit is an **underwriting decision about one customer's creditworthiness**. Repricing the overdraft book and raising one customer's limit are not the same operation and should not look like one.

It is enforced by the types rather than by a rule someone has to remember: the catalogue's pricing record has no limit field at all, so "the limit does not float" is checked by the compiler.

There is a second dividend. Every withdrawal check needs the limit and nothing else — and the limit is on the account's own row, so that path answers in one read with no catalogue lookup. A floating limit would have put a product read on every withdrawal in the system.

The [[day-count|day-count convention]] floats with the product too, because it is part of the price: 12% on \`ACT/365\` and 12% on \`30/360\` are different products.`,
  },
  "pricing-overlay": {
    title: "Pricing overlay",
    body: `A **pricing overlay** is one customer's negotiated price instead of the [[product-catalogue|product]]'s, carried on the account's own [[effective-dated-terms|terms timeline]] rather than in a table of its own — setting one and clearing one are ordinary rows, ordered against limit changes and product migrations like any other.

While an overlay is in force it outranks the product: a reprice published underneath it does not reach that customer. Clearing it puts the account back on the product at **whatever the product costs by then**, not at what it cost when the overlay was set.

No overlay means *float*, and specifically **not** interest-free. A genuinely interest-free account is an overlay with a zero rate, which is a real product and a different, deliberate statement. The two are stored differently for exactly that reason, and confusing them would silently make a whole book free.

The overlay is also **where retroactivity lives**. A backdated overlay moves interest already charged — but to one named customer, and the [[interest-accrual|accrual]] posts the difference as ordinary correction interest rather than rewriting history. The catalogue refuses the same thing outright, because a backdated *publication* would do it to every account on the product at once. Correcting a mispublished rate is therefore laborious, and should be: it is a set of individual decisions about money already taken from named people.`,
  },
  lending: {
    title: "Lending",
    body: `**Lending** is this system's third kind of credit relationship, alongside a plain deposit and an [[overdraft|overdraft limit]]: a [[credit-facility|facility]] whose drawn amount exists as a fact of its own rather than as another account's balance read by sign.

Two products ship: the [[term-loan|term loan]] (fixed principal, one disbursement, a fixed [[amortization|schedule]]) and the [[revolving-line|revolving line]] (a reusable commitment, drawn and repaid repeatedly, billed in cycles). Both accrue [[interest-accrual|interest daily]] on what is actually drawn, settle a [[repayment-allocation|repayment]] against interest before principal, and track [[arrears|arrears]] from their schedule rather than from a log of missed-payment events.

The [[overdraft|arranged overdraft]] is credit too, and older than either — but it is deliberately kept out of this package: its drawn amount is a current account's own negative balance, not an independent fact, so it carries no facility record, no schedule and no commitment.`,
  },
  "term-loan": {
    title: "Term loan",
    body: `A **term loan** is a fixed principal, disbursed once, repaid on an [[amortization|amortization schedule]] generated in full at disbursement — a mortgage or a personal loan. Disbursing debits the bank's loan-principal [[account-type-asset|Asset]] line under this loan and credits wherever the money goes (typically the customer's deposit position); nothing is owed before that posting, and everything scheduled is owed after it.

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
    body: `A **day-count convention** turns a pair of dates into a fraction of a year, and it is a real product parameter, not an implementation detail — literally so: it is a field on a [[product-catalogue|product version]], and it [[pinned-vs-floating|floats]] with the price because it is part of the price — the same balance at the same rate accrues differently under each:

- **\`ACT/365\`** — actual elapsed days over a 365-day year. Most retail lending.
- **\`ACT/360\`** — actual elapsed days over a 360-day year, so a year of daily accrual comes to 365/360 of the nominal rate. Euro money markets, US commercial lending.
- **\`30/360\`** — every month is treated as exactly 30 days and every year as 360, so a calendar month is *always* precisely a twelfth of a year.

That last property is \`30/360\`'s entire purpose: under \`ACT/365\` or \`ACT/360\`, a scheduled monthly instalment's interest and what actually [[interest-accrual|accrues]] over that month generally disagree — a 30-day month accrues less than a flat twelfth, a 31-day one more — and a [[repayment-allocation|repayment]] absorbs the difference in its principal portion. Under \`30/360\` the two always agree to the cent, which is exactly why real mortgages and bonds use it.`,
  },
  "accrued-interest": {
    title: "Accrued interest, at higher precision",
    body: `A day's interest on a real balance is mostly fraction — €10,000 at 6% (\`ACT/365\`) accrues 164.383561 cents a day, not a round number. Rounding that away daily, rather than keeping it, is a real annual error, so a facility or account holds its interest at TWO precisions simultaneously, and the split is the point:

- The **record** — micro-minor-units, minor units × 1,000,000 — is exact and never rounded.
- The **ledger** holds it rounded: to a whole minor unit, half away from zero, because no posting can be a fraction of a cent.

Every day's posting is the CHANGE in that rounded figure, not the day's raw interest, so record and ledger can never drift apart even though most days post one value and occasional days post one more (or less) as the rounding ticks over. See [[capitalization]] for what happens to the residue when this figure is charged.`,
  },
  capitalization: {
    title: "Capitalization",
    body: `**Capitalizing** interest means charging the [[accrued-interest|accrued, rounded]] figure into principal rather than collecting it separately — the interest charge on a [[revolving-line|revolving line]], or the monthly charge on an [[overdraft-interest|overdraft]]. It is what makes either balance **compound**: next period's [[interest-accrual|accrual]] runs on a principal that already includes this period's interest.

Charging the ROUNDED figure rather than the exact one always leaves a residue of up to half a minor unit, in either direction — round down and the record stays slightly positive, round up and it goes slightly negative. That residue rounds to zero — except at an EXACT half of a minor unit, where it rounds AWAY from zero to ±1 even though the ledger is already at zero. That is why closing an account or facility tests its ledger balance in the receivable rather than the record: the record may legitimately disagree with the ledger by a sub-minor-unit amount, which is the entire reason it is kept at higher precision. Ordinarily the next accrual absorbs the residue as the balance moves again. A term loan is never capitalized this way — its interest is settled through its own scheduled instalments instead, never folded back into principal.`,
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
