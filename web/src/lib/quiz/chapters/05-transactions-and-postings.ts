import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "05-transactions-and-postings",
  number: 5,
  part: "Part II · Transactions and Time",
  title: "Transactions and Postings",
  questions: [
    {
      kind: "mc",
      id: "ch5-q1",
      difficulty: "intro",
      concept: "double-entry",
      prompt:
        "A transaction is described as a 'balanced set of entries posted atomically.' What does 'balanced' mean here?",
      options: [
        "The transaction touches the same number of debit and credit accounts",
        "Total debits equal total credits across all legs of the transaction",
        "The transaction must involve exactly two accounts",
        "The ledger has been verified by an auditor",
      ],
      answer: 1,
      explanation:
        "In [[double-entry]] bookkeeping every transaction must balance — total debits must equal total credits, however many legs there are. The *number* of accounts is irrelevant; the *sums* must match.",
    },
    {
      kind: "truefalse",
      id: "ch5-q2",
      difficulty: "intro",
      concept: "reversal",
      prompt:
        "To correct an incorrectly posted transaction, the right procedure is to edit or delete it in the ledger.",
      answer: false,
      explanation:
        "The ledger is **immutable and append-only** — posted transactions are never edited or deleted. To correct an error, a [[reversal]] is posted: a new transaction in which every original debit becomes a credit and vice versa, netting the effect to zero while preserving the full audit trail.",
    },
    {
      kind: "mc",
      id: "ch5-q3",
      difficulty: "intro",
      concept: "idempotency-key",
      prompt:
        "Why does a payment API require callers to supply an idempotency key with each request?",
      options: [
        "To authenticate the caller's identity and authorise the transaction",
        "To prevent the same logical operation from being posted twice if the request is retried",
        "To compress and route the request through the payment network",
        "To assign the transaction to a specific subledger",
      ],
      answer: 1,
      explanation:
        "Networks fail in the worst way: they lose the *response*, not the request. The [[idempotency-key]] is a unique identifier the caller assigns once per logical operation. If the same key arrives twice, the server recognises it and refuses to create a duplicate — the client can then look up the original transaction by that key.",
    },
    {
      kind: "mc",
      id: "ch5-q4",
      difficulty: "intro",
      concept: "ledger-vs-subledger",
      prompt:
        "A bank's General Ledger contains a 'Customer Deposits' subledger holding 50,000 individual accounts. What does the General Ledger show for this subledger?",
      options: [
        "The balance of every individual customer account, listed separately",
        "A combined total balance for all accounts in the subledger",
        "Only the most recently posted transaction across all accounts",
        "An alphabetical index of all customer account names",
      ],
      answer: 1,
      explanation:
        "The [[ledger-vs-subledger]] hierarchy exists so the General Ledger can show one summary total while the subledger holds the individual detail. If Customer Deposits sum to $10 M, the GL shows $10 M — not 50,000 lines. The subledger is where the per-customer granularity lives.",
    },
    {
      kind: "truefalse",
      id: "ch5-q5",
      difficulty: "intro",
      concept: "booking-date",
      prompt:
        "The booking date is the date a transaction is recorded in the system, which may differ from the date it takes economic effect.",
      answer: true,
      explanation:
        "Every transaction carries two dates. The [[booking-date]] is when the system records the transaction; the value date is when it takes economic effect (e.g., when interest starts accruing). The two can diverge by days — a weekend wire transfer or a back-dated correction are common examples.",
    },
    {
      kind: "mc",
      id: "ch5-q6",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A customer sends $100 and the bank charges a $3 transfer fee. The transaction posts three legs: sender debited $100, recipient credited $97, fee-income account credited $3. Why does the system accept this posting?",
      options: [
        "Because the sender's debit equals the recipient's credit ($100 = $97) — the fee is ignored",
        "Because three-leg transactions are always accepted regardless of balance",
        "Because total debits ($100) equal total credits ($97 + $3 = $100)",
        "Because the fee-income account is a revenue account and requires no balancing entry",
      ],
      answer: 2,
      explanation:
        "[[double-entry]] requires total debits = total credits across *all* legs, not just one debit against one credit. The three-leg fee split works because $100 debit = $97 + $3 credits. The number of legs is unlimited; the balance invariant is not.",
    },
    {
      kind: "mc",
      id: "ch5-q7",
      difficulty: "core",
      concept: "idempotency-key",
      prompt:
        "A developer builds a payment feature that sends two separate $50 transfers to the same recipient on behalf of a customer. The developer reuses the same idempotency key for both requests. What happens when the second request is submitted?",
      options: [
        "Both transfers are posted; idempotency deduplication only activates if the amounts and recipients are also identical",
        "The server rejects the second request as a duplicate, even though it represents a genuinely new logical operation",
        "The server merges the two requests into a single $100 transfer and posts it once",
        "The second request is queued and processed after the first transfer is confirmed",
      ],
      answer: 1,
      explanation:
        "An [[idempotency-key]] scopes deduplication to a *logical operation*, not to a specific amount or recipient. The server stores which keys it has already processed and refuses any repeat — regardless of whether the intent was genuinely different. Each distinct logical operation must carry a fresh, unique key; reusing a key is the signal that 'this is the same operation, not a new one.'",
    },
    {
      kind: "truefalse",
      id: "ch5-q8",
      difficulty: "core",
      concept: "reversal",
      prompt:
        "After a reversal transaction is posted, both the original transaction and the reversal remain permanently visible in the ledger.",
      answer: true,
      explanation:
        "The ledger is **append-only** — nothing is ever removed. A [[reversal]] is a new transaction that offsets the original; the original is marked 'reversed' and the reversal carries a back-reference to it. Both records coexist permanently in the audit trail. The net economic effect is zero, but both entries remain visible to auditors, regulators, and investigators.",
    },
    {
      kind: "mc",
      id: "ch5-q9",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt:
        "In the three-leg fee-split transaction (sender debited $100, recipient credited $97, fee-income credited $3), what type of account is fee-income?",
      options: [
        "Asset — the bank now holds $3 more cash",
        "Liability — the bank owes $3 to a third party",
        "Revenue — the fee is income earned by the bank",
        "Expense — the fee represents a processing cost",
      ],
      answer: 2,
      explanation:
        "A transfer fee is income the bank earns for its service. It flows into a [[account-type-revenue]] account, which has a **credit normal balance** — crediting it increases the bank's recognised income. A common error is to think cash is received, so an asset must be credited; the cash leg is elsewhere (or implicit in clearing), and the fee record itself is revenue.",
    },
    {
      kind: "mc",
      id: "ch5-q10",
      difficulty: "core",
      concept: "balance-book",
      prompt:
        "A bookkeeper prepares a trial balance and finds total debits of $50,000 and total credits of $47,000. What does this indicate?",
      options: [
        "The ledger is close enough to balance; a $3,000 rounding difference is acceptable in practice",
        "There is a bookkeeping error somewhere — the double-entry invariant requires total debits to equal total credits exactly",
        "The $3,000 shortfall represents transactions that are pending posting and will reconcile overnight",
        "The trial balance is valid; debit-heavy accounts such as assets routinely create small imbalances",
      ],
      answer: 1,
      explanation:
        "A [[balance-book|trial balance]] is a self-checking tool: because every valid transaction has total debits equal total credits, the aggregate totals across all accounts must also be equal. A $3,000 mismatch proves a bookkeeping error exists somewhere — an amount was entered on the wrong side, a leg was omitted, or a transaction was partially posted. There is no acceptable imbalance; the invariant is absolute.",
    },
    {
      kind: "multi",
      id: "ch5-q11",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "Which of the following are properties of a valid transaction? (Select all that apply.)",
      options: [
        "Total debits equal total credits across all entries",
        "The transaction must have exactly two entries — one debit and one credit",
        "All entries are recorded together atomically — either all succeed or none do",
        "Any number of entries is allowed, provided total debits and credits balance",
        "A transaction may be partially committed if some legs succeed and one fails",
      ],
      answers: [0, 2, 3],
      explanation:
        "[[double-entry]] transactions are **balanced** (debits = credits) and **atomic** (all-or-nothing). They may have any number of legs — a two-legged transfer and a three-legged fee split are equally valid. Partial commits are forbidden: if any leg fails, the whole transaction is rejected and nothing is recorded.",
    },
    {
      kind: "numeric",
      id: "ch5-q12",
      difficulty: "core",
      concept: "amount-cents",
      prompt:
        "A fee-split transaction posts two credit entries: a recipient account credited 9700 units and a fee-income account credited 300 units. Both amounts are in minor currency units (cents). What is the total value of all credits expressed in dollars?",
      answer: 100,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "9700 cents + 300 cents = 10 000 cents. [[amount-cents]] — storing amounts as integer minor units avoids floating-point errors. 10 000 cents ÷ 100 = **$100**. The sender was debited $100, and the two credit legs sum to the same amount, confirming the transaction is balanced.",
    },
    {
      kind: "mc",
      id: "ch5-q13",
      difficulty: "core",
      concept: "idempotency-key",
      prompt:
        "A mobile banking app sends a $75 transfer. The server records the transfer but the response is lost before reaching the app. The app retries with the same idempotency key. What is the correct server behavior?",
      options: [
        "Post a second $75 transfer — the first may not have fully succeeded",
        "Reject the retry permanently as a suspected replay attack",
        "Recognise the key, refuse to post a duplicate, and signal that this key was already used so the app can look up the original",
        "Post a $75 reversal to cancel the first transfer, then post a fresh one",
      ],
      answer: 2,
      explanation:
        "This is the exact problem [[idempotency-key]] solves. A lost response does not mean a lost transaction — the server already committed. Retrying with the **same** key is safe: the server finds the key in its records and signals it was already used. The customer is charged exactly once regardless of network failures.",
      explore: { label: "See payments", href: "/payments" },
    },
    {
      kind: "numeric",
      id: "ch5-q14",
      difficulty: "core",
      concept: "amount-cents",
      prompt:
        "A sender is debited $100 for a transfer that includes a $3 fee. The recipient receives the remainder. By how many dollars does the recipient's account increase?",
      answer: 97,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Debit side: $100. Credit side must also total $100: $3 to fee income + $97 to recipient = $100. [[amount-cents]] records these as 10 000, 300, and 9700 integer cents respectively. The recipient's account is credited **$97**.",
    },
    {
      kind: "truefalse",
      id: "ch5-q15",
      difficulty: "core",
      concept: "normal-balance",
      prompt: "Debiting a liability account always increases its balance.",
      answer: false,
      explanation:
        "Each account type has a [[normal-balance]] direction. Liabilities have a **credit** normal balance: a credit *increases* the liability and a debit *decreases* it. Debiting a customer's deposit account (a liability) reduces the bank's obligation to that customer — it doesn't grow the balance.",
    },
    {
      kind: "multi",
      id: "ch5-q16",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "Which of the following statements correctly describe a reversal transaction? (Select all that apply.)",
      options: [
        "Every debit in the original becomes a credit in the reversal, and vice versa",
        "The reversal carries a reference back to the transaction it cancels",
        "The original transaction is edited so its amounts become zero",
        "The net economic effect across all affected accounts is zero after the reversal is posted",
        "The original transaction disappears from audit logs once reversed",
      ],
      answers: [0, 1, 3],
      explanation:
        "A [[reversal]] exactly mirrors the original (debit↔credit flip) and references the transaction it cancels. Because it is a perfect mirror, the combined net effect on every affected account is zero. The ledger is append-only: the original is never edited or removed — only marked 'reversed.' Both records remain in audit logs permanently.",
    },
    {
      kind: "mc",
      id: "ch5-q17",
      difficulty: "challenge",
      prompt:
        "A system receives a three-leg posting: sender debited $100, recipient credited $60, fee-income credited $30. What does the system do?",
      options: [
        "Posts the transaction and flags it for manual review, since the imbalance is small",
        "Rejects the entire transaction — no entries are recorded",
        "Posts the legs that fit within the debit total and discards the remainder",
        "Automatically adds a balancing credit entry to make up the $10 difference",
      ],
      answer: 1,
      explanation:
        "The ledger enforces the balance invariant strictly: total debits must equal total credits. Here debits = $100 but credits = $60 + $30 = $90 — a $10 shortfall. The system rejects the **entire** transaction atomically; no partial posting occurs. The caller must correct the entries and resubmit with a balanced set.",
    },
    {
      kind: "numeric",
      id: "ch5-q18",
      difficulty: "challenge",
      concept: "amount-cents",
      prompt:
        "A $10,000 loan disbursement is recorded as a single multi-leg transaction: loan-receivable asset debited $10,000; customer deposit credited $10,000; customer deposit debited $200 (origination fee); fee-revenue credited $200. What is the net credit to the customer's deposit account in dollars?",
      answer: 9800,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The customer's deposit receives two legs in the same transaction: a credit of $10,000 and a debit of $200 (origination fee). Net: $10,000 − $200 = **$9,800**. [[amount-cents]] arithmetic: 1 000 000 − 20 000 = 980 000 cents = $9,800. All four legs balance: total debits ($10,000 + $200) = total credits ($10,000 + $200) = $10,200.",
    },
    {
      kind: "multi",
      id: "ch5-q19",
      difficulty: "challenge",
      concept: "balance-book",
      prompt:
        "A trial balance lists: Loan Receivable (Asset) $15,000 debit; Customer Deposits (Liability) $12,000 credit; Fee Income (Revenue) $3,000 credit. Which of the following statements are true? (Select all that apply.)",
      options: [
        "The trial balance is balanced: total debits ($15,000) equal total credits ($12,000 + $3,000)",
        "The balanced trial balance proves that all transactions were posted to the correct accounts",
        "A trial balance that balances guarantees no bookkeeping errors exist anywhere",
        "Any new posting — including a reversal — must itself be balanced, or it will break the trial balance",
      ],
      answers: [0, 3],
      explanation:
        "The [[balance-book]] invariant: total debits must equal total credits in a trial balance. Here $15,000 Dr = $12,000 + $3,000 Cr — it is balanced. However, balance only proves the *sum* is correct; it does NOT detect compensating errors such as debiting the wrong account with the right amount. Every new posting, including a reversal, must itself be balanced to preserve the invariant.",
    },
    {
      kind: "mc",
      id: "ch5-q20",
      difficulty: "challenge",
      concept: "account-type-expense",
      prompt:
        "A bank posts monthly savings interest: customer deposit credited $100 and a tax-withholding liability credited $20. Which entry completes this transaction so it balances?",
      options: [
        "Debit interest-expense $80 — only the net amount paid to the customer is expensed",
        "Debit interest-expense $120 — the full obligation including tax withholding",
        "Credit interest-expense $120 — revenue accounts increase with credits",
        "Debit cash $120 — the interest is paid directly from the bank's cash reserves",
      ],
      answer: 1,
      explanation:
        "Total credits = $100 + $20 = $120. The balancing debit must also be $120, going to interest-expense. [[account-type-expense]] accounts have a **debit normal balance** — debiting one increases the bank's recognised cost. The $20 tax is withheld on behalf of the customer (a liability the bank owes to the tax authority), not deducted from the bank's expense — the bank still incurred the full $120 interest obligation.",
    },
  ],
};
