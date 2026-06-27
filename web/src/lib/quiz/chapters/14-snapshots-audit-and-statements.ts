import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "14-snapshots-audit-and-statements",
  number: 14,
  part: "Part V · Records and Reporting",
  title: "Snapshots, Audit Trails, and Statements",
  questions: [
    {
      kind: "mc",
      id: "ch14-q1",
      difficulty: "intro",
      concept: "snapshot",
      prompt:
        "What does an end-of-day snapshot record for each deposit account?",
      options: [
        "The value-date book balance, holds, and available balance at close of business",
        "Only the transactions posted since the previous business day",
        "The booking-date book balance only",
        "The account's full transaction history since opening",
      ],
      answer: 0,
      explanation:
        "A [[snapshot]] captures the three-part deposit balance — value-date book balance, holds, and available balance — at close of business. The value-date balance is used (not the booking-date balance) because it is the economically real position that drives interest accrual and regulatory reporting.",
    },
    {
      kind: "multi",
      id: "ch14-q2",
      difficulty: "intro",
      prompt:
        "Which of the following are purposes served by end-of-day snapshots? (Select all that apply.)",
      options: [
        "Daily interest accrual — balance × rate / 365",
        "Monthly statement generation",
        "Regulatory position reporting",
        "Performance: start balance queries from a recent checkpoint rather than replaying all events from account opening",
        "Preventing new payments from posting until the snapshot is complete",
      ],
      answers: [0, 1, 2, 3],
      explanation:
        "[[snapshot|Snapshots]] serve four purposes: interest accrual, statement generation, regulatory reporting, and query performance (balance at any date is read from the nearest snapshot plus subsequent transactions). Snapshots do not block payment processing — payments post to the ledger independently.",
    },
    {
      kind: "truefalse",
      id: "ch14-q3",
      difficulty: "intro",
      concept: "snapshot",
      prompt:
        "An end-of-day snapshot records the value-date balance, not the booking-date balance.",
      answer: true,
      explanation:
        "[[snapshot|Snapshots]] capture the **value-date** balance — the economically real position. Interest and regulation care about when money is economically effective, not merely when an entry was logged. The booking-date balance can differ when a posting is recorded before its value date arrives.",
    },
    {
      kind: "mc",
      id: "ch14-q4",
      difficulty: "intro",
      concept: "statement",
      prompt:
        "On a bank statement, the transaction listing is ordered by which date?",
      options: [
        "Value date — when money becomes economically effective",
        "Booking date — when the transaction was recorded in the ledger",
        "Settlement date — when interbank reserves moved",
        "The date the customer authorized the transaction",
      ],
      answer: 1,
      explanation:
        "The [[statement]] transaction listing uses **booking date** — the date the entry appeared in the ledger, which is the date the customer associates with their activity (\"I made this payment on Feb 3rd\"). Opening and closing balances, however, use value date.",
    },
    {
      kind: "multi",
      id: "ch14-q5",
      difficulty: "intro",
      concept: "statement",
      prompt:
        "Which of the following appear on a bank statement? (Select all that apply.)",
      options: [
        "Transaction listing ordered by booking date",
        "Opening and closing balances computed using value date",
        "Daily balances derived from end-of-day snapshots",
        "Authorization holds outstanding at period end",
        "The account's configured overdraft limit",
      ],
      answers: [0, 1, 2],
      explanation:
        "A [[statement]] shows three things: the transaction listing (booking date), opening/closing balances (value date), and daily balances from [[snapshot|snapshots]]. Holds never appear — a hold posts nothing to the ledger until captured, so a ledger-derived statement is blind to it. The overdraft limit is a deposit-layer config, not a statement line.",
    },
    {
      kind: "numeric",
      id: "ch14-q6",
      difficulty: "core",
      prompt:
        "A savings account holds $36,500 and earns 4% annual interest. Using the snapshot model (daily interest = principal × rate / 365), how many dollars of interest accrue for one day? (Enter a number of dollars.)",
      answer: 4,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Daily interest = $36,500 × 0.04 / 365 = $1,460 / 365 = **$4.00**. The end-of-day [[snapshot]] provides the exact principal for each day's accrual calculation. Without a captured end-of-day figure there is nothing to accrue against.",
    },
    {
      kind: "mc",
      id: "ch14-q7",
      difficulty: "core",
      concept: "statement-amount",
      prompt:
        "A customer's February statement shows an opening balance of $2,000. During February, $850 in credits and $300 in debits are posted — all with value dates within February. What is the February closing balance?",
      options: ["$2,550", "$1,450", "$2,000 — the opening balance carries forward unchanged", "$2,300"],
      answer: 0,
      explanation:
        "[[statement-amount|Closing balance]] = opening + credits − debits, counting only postings whose value date falls within the period. $2,000 + $850 − $300 = **$2,550**. Debits reduce the balance; credits increase it.",
    },
    {
      kind: "truefalse",
      id: "ch14-q8",
      difficulty: "core",
      concept: "statement-amount",
      prompt:
        "A transaction booked on February 25 with a value date of March 1 appears in the February statement's transaction listing but does NOT affect February's closing balance. This is correct behavior.",
      answer: true,
      explanation:
        "The [[statement-amount|closing balance]] is built from value-date snapshots. The transaction appears in February's listing because the customer performed it in February (booking date); it does not affect February's closing balance because it becomes economically real only on March 1 (value date). This design is why most statements print both dates when they differ.",
    },
    {
      kind: "mc",
      id: "ch14-q9",
      difficulty: "core",
      concept: "value-date",
      prompt:
        "A transaction is booked on January 31 with a value date of February 1. On the February statement, where does this transaction appear?",
      options: [
        "In the February listing and affecting the February opening balance",
        "In the February listing only — it has no effect on any February balance",
        "Not in the February listing, but it is part of the February opening balance",
        "In neither the February listing nor the February balances — it belongs entirely to January",
      ],
      answer: 2,
      explanation:
        "The February [[statement]] lists transactions by booking date. This posting was booked January 31, so it does not appear in February's listing. However, its [[value-date]] is February 1, which makes it economically real in February — so it is reflected in the February opening balance. (The January 31 end-of-day snapshot captures only value dates ≤ January 31; this February 1-valued posting falls outside that snapshot and lands in the February 1 daily balance, which is the effective opening of the February period.)",
    },
    {
      kind: "mc",
      id: "ch14-q10",
      difficulty: "core",
      concept: "ledger-vs-subledger",
      prompt:
        "In a layered core banking system, which layer is responsible for capturing end-of-day snapshots, and why?",
      options: [
        "The general ledger, because it records all postings",
        "The deposit layer, because snapshots record the three-part balance (book, holds, available) which only the deposit layer tracks",
        "The payment layer, because snapshots are used for settlement reporting",
        "A dedicated snapshot service that sits outside the ledger hierarchy",
      ],
      answer: 1,
      explanation:
        "[[ledger-vs-subledger|Snapshots belong to the deposit layer]] (`deposit.Register`). The pure general ledger computes book balances on demand but stores no snapshots — it knows nothing about holds or available balances. Because the deposit layer owns the three-part balance, it also owns the [[snapshot]].",
    },
    {
      kind: "numeric",
      id: "ch14-q11",
      difficulty: "core",
      concept: "statement-amount",
      prompt:
        "An account's end-of-January snapshot shows a value-date book balance of $1,500. In February, three credits land with February value dates: $200, $350, and $100. One debit of $400 also posts with a February value date. What is the February closing balance in dollars?",
      answer: 1750,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[statement-amount|Closing balance]] = opening snapshot + credits − debits, measured by value date. $1,500 + ($200 + $350 + $100) − $400 = $1,500 + $650 − $400 = **$1,750**.",
    },
    {
      kind: "mc",
      id: "ch14-q12",
      difficulty: "core",
      concept: "booking-date",
      prompt:
        "A salary credit arrives at the bank with a booking date of April 30 and a value date of May 1. On which statement does this credit appear in the transaction listing, and what is the effect on the April closing balance?",
      options: [
        "April listing — and it increases the April closing balance",
        "April listing — but it does NOT affect the April closing balance",
        "May listing — and it increases the May opening balance instead",
        "Neither listing — cross-month transactions require manual reconciliation",
      ],
      answer: 1,
      explanation:
        "The [[booking-date]] (April 30) determines which period's listing includes this transaction — it appears in April. The [[value-date]] (May 1) determines economic effect — the credit becomes real in May, so it does not affect April's closing balance. Instead it contributes to the May opening balance. This is why a statement's transaction listing and its opening-to-closing balance change can diverge.",
    },
    {
      kind: "truefalse",
      id: "ch14-q13",
      difficulty: "core",
      concept: "statement",
      prompt:
        "An authorization hold placed on an account appears as a line item on the account's bank statement.",
      answer: false,
      explanation:
        "[[statement|Statements]] are derived from the general ledger; a hold posts nothing to the ledger until it is captured. Until capture, the hold exists only in the deposit layer and is invisible to the ledger-based statement. Only real book movements — including captures — appear as statement lines.",
    },
    {
      kind: "mc",
      id: "ch14-q14",
      difficulty: "core",
      concept: "balance-book",
      prompt:
        "When a statement's running balance is accumulated from oldest to newest posting, what built-in correctness check does this create?",
      options: [
        "The final running balance must match the account's current book balance — any mismatch reveals a bug or data corruption",
        "The running balance must equal zero because all debits and credits cancel out over time",
        "Each line's running balance must exceed the previous line's balance (no net debits allowed)",
        "The total credits in the period must exactly match the total debits in the period",
      ],
      answer: 0,
      explanation:
        "[[balance-book|The book balance]] and the statement's accumulated running balance are independently derived from the same underlying ledger. At statement end, both figures must agree — this is the account-level equivalent of a trial balance. A mismatch signals a data integrity problem.",
    },
    {
      kind: "multi",
      id: "ch14-q15",
      difficulty: "core",
      prompt:
        "Which of the following events are recorded in the bank's append-only audit trail? (Select all that apply.)",
      options: [
        "Account opening",
        "Every debit and credit posting",
        "Authorization holds placed and released",
        "Reversals of erroneous transactions",
        "End-of-day snapshot captures",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "The audit trail is an immutable, append-only log of **every** system mutation — not just postings. Account lifecycle events, all postings, hold operations, reversals, and [[snapshot]] captures all appear. This completeness is what enables full event replay to independently recompute any historical state.",
    },
    {
      kind: "mc",
      id: "ch14-q16",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "A $200 erroneous credit is posted to an account on March 5, then reversed on March 7. How does this appear on the March statement?",
      options: [
        "As a single net-zero entry on March 5 — the system automatically collapses corrected pairs",
        "As two separate lines: +$200 on March 5 and −$200 on March 7, both visible",
        "The March 5 entry disappears; only the reversal line is visible",
        "As a comment on the original March 5 line rather than a separate transaction",
      ],
      answer: 1,
      explanation:
        "A [[reversal]] is a brand-new, equal-and-opposite posting — the ledger is immutable and append-only, so the original entry cannot be removed or edited. Both the original and the reversal appear as separate statement lines. Their net effect is zero, but both lines remain visible, preserving the full audit trail.",
    },
    {
      kind: "mc",
      id: "ch14-q17",
      difficulty: "challenge",
      concept: "snapshot",
      prompt:
        "A bank suspects silent data corruption may have affected some account balances. What is the strongest available correctness check?",
      options: [
        "Compare today's snapshot to yesterday's snapshot and verify the difference equals today's postings",
        "Replay every event in the immutable audit trail from the beginning, recompute each balance from scratch, and compare against currently reported figures",
        "Ask a sample of customers to confirm their displayed balance is correct",
        "Run a trial balance and verify that total debits equal total credits across all accounts",
      ],
      answer: 1,
      explanation:
        "Replaying the full audit trail from first principles independently derives what every balance *must* be, without trusting any cached or [[snapshot|snapshotted]] value. If a recomputed figure disagrees with the stored balance, something is corrupt. A trial balance (option D) only confirms debit-credit equality — it cannot catch whether a particular account's balance is wrong.",
    },
    {
      kind: "numeric",
      id: "ch14-q18",
      difficulty: "challenge",
      prompt:
        "An account's April 30 end-of-day snapshot shows a value-date book balance of $3,000. In May, the following postings occur: a $100 credit booked April 28 with value date May 2; a $500 credit with value date May 3; a $200 debit with value date May 10; a $150 credit with value date May 20. What is the May closing balance in dollars?",
      answer: 3550,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The April 30 snapshot captures balances for value dates ≤ April 30 only. The April 28-booked / May 2-value credit was NOT in the snapshot — its value date is in May, so it contributes to May. May closing = $3,000 + $100 + $500 − $200 + $150 = **$3,550**. This is a concrete example of why the statement-amount calculation counts postings by value date, not booking date.",
    },
    {
      kind: "truefalse",
      id: "ch14-q19",
      difficulty: "challenge",
      concept: "double-entry",
      prompt:
        "Because the ledger is both double-entry and append-only, every account's current balance can be independently verified by replaying the recorded event history from scratch.",
      answer: true,
      explanation:
        "[[double-entry]] ensures every posting is balanced (debits = credits), and the append-only audit trail ensures no event is ever silently removed or altered. Together, these properties make the entire current state derivable from recorded history — the strongest form of trustworthiness a [[snapshot|banking record]] can offer.",
    },
    {
      kind: "mc",
      id: "ch14-q20",
      difficulty: "challenge",
      concept: "value-date",
      prompt:
        "A careful customer adds up all transactions in their February statement listing and finds the total does not equal the change from opening to closing balance. What is the most likely explanation?",
      options: [
        "The bank made an arithmetic error in the closing balance",
        "Some listed transactions have a value date in March (so they do not affect February's closing balance), and/or some transactions that value-dated in February were booked in January (so they do not appear in the February listing)",
        "Holds are included in the opening balance but excluded from the closing balance",
        "The statement engine incorrectly applied the booking-date balance instead of the value-date balance",
      ],
      answer: 1,
      explanation:
        "The [[statement]] listing uses booking date; balances use [[value-date|value date]]. A transaction booked in February but value-dating in March appears in the listing but does not move the closing balance. A transaction booked in January but value-dating in February moves the opening balance but does not appear in the listing. Both effects cause the listed transactions to not 'add up' — by design, not by error.",
    },
  ],
};
