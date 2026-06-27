import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "08-account-lifecycle-and-overdraft",
  number: 8,
  part: "Part III · Accounts Over a Lifetime",
  title: "The Account Lifecycle and Overdraft",
  questions: [
    {
      kind: "mc",
      id: "ch8-q1",
      difficulty: "intro",
      concept: "account-status",
      prompt:
        "Which account status is the fully operational state — accepting debits, credits, and new hold authorizations?",
      options: ["Dormant", "Frozen", "Active", "Closed"],
      answer: 2,
      explanation:
        "An [[account-status|Active]] account is the fully operational state. [[account-status|Dormant]] restricts to credits only; [[account-status|Frozen]] permits only balance viewing; [[account-status|Closed]] accepts no transactions at all.",
    },
    {
      kind: "mc",
      id: "ch8-q2",
      difficulty: "intro",
      concept: "account-status",
      prompt: "A salary credit arrives for a Dormant account. What happens?",
      options: [
        "The credit is rejected — dormant accounts accept no transactions",
        "The credit is accepted and the account reactivates to Active",
        "The credit is accepted but the account stays Dormant",
        "The credit is held pending an explicit reactivation request",
      ],
      answer: 1,
      explanation:
        "[[account-status|Dormant]] accounts accept incoming credits — and any credit (or customer-initiated activity) triggers reactivation to Active. This ensures legitimate incoming funds never bounce off a forgotten account.",
    },
    {
      kind: "mc",
      id: "ch8-q3",
      difficulty: "core",
      concept: "account-status",
      prompt:
        "An account was Dormant when a fraud alert caused it to be Frozen. The alert is cleared and the freeze lifted. Which state does the account return to?",
      options: [
        "Active — unfreeze always resets to the default operating state",
        "Dormant — the Frozen state remembers and restores the prior state",
        "Closed — a fraud flag triggers account termination",
        "It remains Frozen until an explicit reactivation request from the customer",
      ],
      answer: 1,
      explanation:
        "The [[account-status|Frozen]] state remembers the previous state. Lifting the freeze restores the account to what it was before — Dormant in this case. An account that was Active when frozen would return to Active; Dormant returns to Dormant.",
    },
    {
      kind: "mc",
      id: "ch8-q4",
      difficulty: "intro",
      concept: "overdraft",
      prompt:
        "A customer with no overdraft facility tries to debit $600 from an account with a $400 available balance. What happens?",
      options: [
        "The transaction posts and the book balance becomes −$200",
        "The transaction is hard-declined before any ledger entry is made",
        "A $600 hold is placed and the debit is queued for later",
        "$400 is debited and the remaining $200 is returned as a partial rejection",
      ],
      answer: 1,
      explanation:
        "Without an arranged [[overdraft]], the system applies a hard decline — the transaction is refused before reaching the ledger. The [[balance-available|available balance]] is the gate; a debit that would push it below zero is rejected outright with an insufficient-funds error.",
    },
    {
      kind: "mc",
      id: "ch8-q5",
      difficulty: "core",
      concept: "overdraft",
      prompt:
        "A customer has a $0 book balance and a $500 arranged overdraft limit. A $300 debit is approved. What is the resulting book balance?",
      options: [
        "$0 — the overdraft limit absorbs the debit without touching the book balance",
        "−$300 — the customer now owes the bank $300",
        "+$300 — the bank credits the account for providing the facility",
        "$500 — the full limit is drawn down immediately",
      ],
      answer: 1,
      explanation:
        "An arranged [[overdraft]] allows the book balance to go negative. After the $300 debit, the book balance is −$300, meaning the customer owes the bank $300. The remaining overdraft capacity is $200 ($500 limit minus $300 used).",
    },
    {
      kind: "mc",
      id: "ch8-q6",
      difficulty: "core",
      concept: "balance-available",
      prompt:
        "Which formula correctly expresses the available balance for an account with an arranged overdraft facility?",
      options: [
        "Book balance − Active holds",
        "Book balance + Overdraft limit",
        "Book balance + Overdraft limit − Active holds",
        "Book balance − Overdraft limit − Active holds",
      ],
      answer: 2,
      explanation:
        "[[balance-available]] = Book balance + Overdraft limit − Active holds. The overdraft limit raises the floor of what is spendable; [[holds]] lower it. Without an overdraft facility, the limit term is zero, simplifying to the familiar book-minus-holds formula.",
    },
    {
      kind: "mc",
      id: "ch8-q7",
      difficulty: "core",
      concept: "account-type-liability",
      prompt:
        "A deposit account's book balance falls from +$500 to −$200 after an authorized overdraft debit. From the bank's perspective, which best describes the resulting −$200 position?",
      options: [
        "A $200 liability — the bank still owes the customer $200",
        "A $200 asset — the customer now owes the bank $200",
        "A $200 equity reduction recorded against the bank's capital",
        "No balance-sheet impact — overdraft entries are off-balance-sheet",
      ],
      answer: 1,
      explanation:
        "A deposit is normally an [[account-type-liability]] — the bank owes the customer. When the book balance turns negative, the relationship inverts: the customer owes the bank, so the outstanding amount appears as a receivable (an asset) on the bank's balance sheet. A negative liability is economically equivalent to an asset.",
    },
    {
      kind: "mc",
      id: "ch8-q8",
      difficulty: "core",
      concept: "holds",
      prompt:
        "An account manager tries to close an Active account that has a zero book balance but one unresolved hold authorization. What should happen?",
      options: [
        "The account closes — the book balance is what matters for closure",
        "The close is rejected — all outstanding holds must be resolved first",
        "The hold is automatically released and the account closes immediately",
        "The hold is converted to a debit entry before closure proceeds",
      ],
      answer: 1,
      explanation:
        "Closing an account requires the book balance to be exactly zero **and** all [[holds]] to be resolved (and all scheduled payments cancelled). An outstanding hold represents funds reserved but not yet captured or released; the account cannot be closed until the hold is settled.",
    },
    {
      kind: "mc",
      id: "ch8-q9",
      difficulty: "challenge",
      concept: "balance-available",
      prompt:
        "An account has a book balance of −$400, an arranged overdraft limit of $500, and no active holds. A $200 debit arrives. There is no unarranged overdraft coverage. What is the outcome?",
      options: [
        "Approved — the book balance of −$400 is still within the $500 limit",
        "Declined — the available balance is only $100, which is insufficient for a $200 debit",
        "Approved as a courtesy unarranged extension at a higher fee",
        "Queued — the system waits for the balance to recover before retrying",
      ],
      answer: 1,
      explanation:
        "Available balance = book (−$400) + limit ($500) − holds ($0) = $100. The requested $200 exceeds the available $100, so the debit is declined. The overdraft limit does not allow unlimited spending — once the available balance is exhausted the account behaves like one with no overdraft facility.",
    },
    {
      kind: "mc",
      id: "ch8-q10",
      difficulty: "challenge",
      concept: "balance-book",
      prompt:
        "Why does the overdraft authorization gate check the available balance rather than the book balance?",
      options: [
        "The book balance is only settled at end of day, so it lags real-time",
        "Active holds represent committed funds not yet deducted from the book balance; ignoring them would allow double-spending",
        "Regulators require available-balance checks for all credit facilities",
        "The book balance is inaccessible to the business layer during authorization",
      ],
      answer: 1,
      explanation:
        "[[balance-book|Book balance]] does not subtract active [[holds]] — a hold reserves funds without yet debiting the ledger. Checking only the book balance would let a customer spend the same reserved funds twice. The available balance formula (book + overdraft limit − holds) captures the true spendable position.",
    },
    {
      kind: "truefalse",
      id: "ch8-q11",
      difficulty: "intro",
      concept: "reversal",
      prompt:
        "A payment posted to an account can be reversed even after the account has been Closed.",
      answer: false,
      explanation:
        "A [[account-status|Closed]] account is a terminal state that accepts no transactions — including [[reversal|reversals]]. Any correction to entries associated with a closed account must be routed through a separate open account. The historical ledger entry remains intact; the account is simply no longer writable.",
    },
    {
      kind: "truefalse",
      id: "ch8-q12",
      difficulty: "intro",
      concept: "overdraft",
      prompt:
        "With an arranged overdraft limit of $300, a deposit account's book balance can legitimately reach −$300.",
      answer: true,
      explanation:
        "An arranged [[overdraft]] explicitly permits the book balance to go negative, down to the negative of the limit. At −$300 exactly, the entire limit is consumed but not breached. The customer owes the bank $300 — economically this is a short-term loan.",
    },
    {
      kind: "truefalse",
      id: "ch8-q13",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "From a pure accounting standpoint, an overdraft debit on a deposit account requires a matching credit entry somewhere else in the ledger — the same rule that governs every other posting.",
      answer: true,
      explanation:
        "[[double-entry]] is universal: every debit must be matched by an equal credit elsewhere. An overdraft debit to the deposit account — whether it leaves the balance positive or drives it negative — is always offset by a credit to another account (for example, the payment sent to a counterparty or funds released as cash). Going negative changes the economic relationship between customer and bank, but it does not relax the bookkeeping structure.",
    },
    {
      kind: "truefalse",
      id: "ch8-q14",
      difficulty: "challenge",
      concept: "double-entry",
      prompt:
        "When a debit drives a deposit account's balance below zero, the double-entry accounting principle is violated because there is no matching credit.",
      answer: false,
      explanation:
        "[[double-entry]] is never violated. A debit to the deposit account (reducing the liability or creating a receivable) is always matched by an equal credit elsewhere — for example, cash paid out or another account credited. Going negative changes the economic interpretation but not the bookkeeping structure; every entry remains balanced.",
    },
    {
      kind: "multi",
      id: "ch8-q15",
      difficulty: "core",
      prompt:
        "Which conditions must ALL be satisfied before an Active account can be Closed? Select all that apply.",
      options: [
        "The book balance must be exactly zero",
        "All active hold authorizations must be resolved",
        "The account must have been Active for at least 30 days",
        "All scheduled payments must be cancelled",
        "The account must pass through Dormant before it can be Closed",
      ],
      answers: [0, 1, 3],
      explanation:
        "Closing requires three conditions: the [[balance-book|book balance]] must be exactly zero, all [[holds]] must be resolved, and all scheduled payments must be cancelled. There is no minimum operating age and no mandatory Dormant step — [[account-status|Closed]] is reached directly from Active once these financial conditions are met.",
    },
    {
      kind: "multi",
      id: "ch8-q16",
      difficulty: "core",
      prompt:
        "Which account states block incoming credit transfers entirely? Select all that apply.",
      options: [
        "Active",
        "Dormant",
        "Frozen (full freeze)",
        "Closed",
      ],
      answers: [2, 3],
      explanation:
        "[[account-status|Active]] accepts all transactions; [[account-status|Dormant]] explicitly accepts credits and uses them to trigger reactivation. A fully [[account-status|Frozen]] account permits only balance viewing — all postings, including credits, are blocked. [[account-status|Closed]] accepts nothing. (A partial freeze may allow credits while blocking debits, but a full freeze does not.)",
    },
    {
      kind: "multi",
      id: "ch8-q17",
      difficulty: "challenge",
      prompt:
        "An account has a book balance of $200 and an arranged overdraft limit of $500. A $600 debit is approved and posts (no active holds). Which of the following statements are true? Select all that apply.",
      options: [
        "The customer now owes the bank $400",
        "The overdraft limit has been breached",
        "The bank records the outstanding amount as a receivable",
        "$100 of overdraft capacity remains after the transaction",
        "The debit should have been declined — it exceeded the original book balance",
      ],
      answers: [0, 2, 3],
      explanation:
        "Available before = $200 + $500 − $0 = $700 ≥ $600, so the debit is approved. After posting: book balance = −$400 (customer owes bank $400 — A is true); the bank holds a $400 receivable (C is true); remaining available = −$400 + $500 = $100 (D is true). The limit ($500) is not breached since only $400 is used (B is false). The debit is valid because the available balance — not the book balance — is the gate (E is false).",
    },
    {
      kind: "numeric",
      id: "ch8-q18",
      difficulty: "core",
      prompt:
        "An account has a book balance of $50, an arranged overdraft limit of $300, and active holds totalling $80. What is the available balance in dollars?",
      answer: 270,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[balance-available]] = Book balance + Overdraft limit − Active holds = $50 + $300 − $80 = **$270**. The overdraft limit adds to spendable capacity; active [[holds]] reduce it.",
    },
    {
      kind: "numeric",
      id: "ch8-q19",
      difficulty: "core",
      prompt:
        "An account starts with a book balance of $200 and an arranged overdraft limit of $500 (no active holds). A $600 debit posts. How many dollars does the customer now owe the bank?",
      answer: 400,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Available before the debit = $200 + $500 − $0 = $700 ≥ $600, so the transaction is approved. Book balance after = $200 − $600 = −$400. A negative book balance means the customer owes the bank **$400** — the [[overdraft]] has flipped the relationship.",
    },
    {
      kind: "numeric",
      id: "ch8-q20",
      difficulty: "challenge",
      prompt:
        "An account has a book balance of −$350, an arranged overdraft limit of $500, and active holds of $100. How many dollars can still be debited before the overdraft limit is fully utilized?",
      answer: 50,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[balance-available]] = book (−$350) + limit ($500) − holds ($100) = **$50**. This is the maximum additional debit permitted before the available balance reaches zero and the overdraft capacity is exhausted.",
    },
  ],
};
