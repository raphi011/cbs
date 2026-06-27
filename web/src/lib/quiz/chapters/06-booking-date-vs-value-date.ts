import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "06-booking-date-vs-value-date",
  number: 6,
  part: "Part II · Transactions and Time",
  title: "Booking Date vs. Value Date",
  questions: [
    {
      kind: "mc",
      id: "ch6-q1",
      difficulty: "intro",
      concept: "booking-date",
      prompt: "What does the booking date represent in a banking transaction?",
      options: [
        "The date funds become available to the customer",
        "The date the transaction was recorded in the system",
        "The date interest starts accruing on the account",
        "The date the bank notifies the customer of the transaction",
      ],
      answer: 1,
      explanation:
        "The [[booking-date]] is purely a processing timestamp — it records when the system captured the entry. Economic effects such as interest accrual and fund availability follow the [[value-date]], not the booking date.",
    },
    {
      kind: "mc",
      id: "ch6-q2",
      difficulty: "intro",
      concept: "value-date",
      prompt: "Which of the following is primarily governed by the value date?",
      options: [
        "When the transaction appears in audit trails and operational reports",
        "When the bank's back-office team reviews the transaction",
        "When interest starts accruing and funds become economically available",
        "When the customer receives a statement notification",
      ],
      answer: 2,
      explanation:
        "The [[value-date]] is the economic effective date. Interest accrual, fund availability, and period-end regulatory reporting all follow the value date, not the [[booking-date]].",
    },
    {
      kind: "truefalse",
      id: "ch6-q3",
      difficulty: "intro",
      prompt:
        "For a simple cash deposit made in a branch, the booking date and value date coincide.",
      answer: true,
      explanation:
        "Cash deposits are the textbook case where the two dates are identical — funds are immediately available and interest begins the same moment they are recorded. Divergence arises only when clearing delays, settlement lags, or explicit back-dating is involved.",
    },
    {
      kind: "mc",
      id: "ch6-q4",
      difficulty: "intro",
      concept: "settlement-delay",
      prompt:
        "A wire transfer arrives Friday evening after the cut-off. Over a long holiday weekend, how far can the gap between booking date and value date stretch?",
      options: [
        "1 calendar day",
        "2 calendar days",
        "3 calendar days",
        "4 to 5 calendar days",
      ],
      answer: 3,
      explanation:
        "A Friday evening booking and a next-business-day [[value-date]] gives a 3-day gap for a regular weekend. A long holiday weekend can push the [[settlement-delay]] to 4 or 5 calendar days before funds take economic effect.",
    },
    {
      kind: "truefalse",
      id: "ch6-q5",
      difficulty: "intro",
      prompt:
        "A check deposited on Monday and immediately recorded by the bank earns interest from Monday onward.",
      answer: false,
      explanation:
        "Interest follows the [[value-date]], not the [[booking-date]]. A check booked on Monday but value-dated Wednesday earns no interest until Wednesday — the booking date is purely a processing timestamp with no bearing on when interest begins.",
    },
    {
      kind: "mc",
      id: "ch6-q6",
      difficulty: "core",
      concept: "booking-date",
      prompt:
        "An operations team needs to produce an audit trail listing every transaction entered in the system during March. Which date field should they filter on?",
      options: [
        "Value date — because it governs when each transaction took economic effect",
        "Booking date — because it records when each transaction was entered in the system",
        "Either date — the two are always identical for audit purposes",
        "Settlement date — the date each transaction became irrevocable",
      ],
      answer: 1,
      explanation:
        "Audit trails and operational reports are ordered by [[booking-date]] — the timestamp recording when the system captured each entry. The [[value-date]] governs interest accrual, fund availability, and period-end balance calculations, not system-entry ordering. Because the two dates can diverge by days or weeks (e.g. a standing order booked today but value-dated next month), filtering on value date would misclassify transactions that were entered in March but take economic effect later.",
    },
    {
      kind: "truefalse",
      id: "ch6-q7",
      difficulty: "core",
      concept: "value-date",
      prompt:
        "The value date of a transaction must always fall on or after its booking date.",
      answer: false,
      explanation:
        "The [[value-date]] can be *earlier* than the [[booking-date]]. Back-dated corrections are a common example: the bank records the fix today but assigns a past value date so that interest and balances are correctly restated for the historical period.",
    },
    {
      kind: "mc",
      id: "ch6-q8",
      difficulty: "core",
      concept: "value-date",
      prompt:
        "An operations team discovers on March 5 that a corporate payment should have settled on February 28. Which value date should the correction entry carry?",
      options: [
        "March 5 — the date the correction is booked",
        "February 28 — the date the economic event should have occurred",
        "February 1 — the first day of the month in which the error falls",
        "The next business day after March 5",
      ],
      answer: 1,
      explanation:
        "Back-dated corrections carry the [[value-date]] of the original economic event (February 28) even though the [[booking-date]] is March 5. This ensures interest for the intervening days is calculated correctly and the historical balance position is accurate.",
    },
    {
      kind: "mc",
      id: "ch6-q9",
      difficulty: "core",
      concept: "booking-date",
      prompt:
        "A customer creates a standing order on January 20 to pay rent on February 1. Which statement correctly describes the two dates?",
      options: [
        "Booking date January 20; value date February 1",
        "Both dates are January 20 — when the instruction was captured",
        "Booking date February 1; value date January 20",
        "Both dates are February 1 — when money actually moves",
      ],
      answer: 0,
      explanation:
        "The instruction is captured and recorded immediately — [[booking-date]] = January 20. Money does not move and interest implications do not begin until February 1 — [[value-date]] = February 1. This forward-dated standing order shows that the value date can lie well after the booking date.",
    },
    {
      kind: "truefalse",
      id: "ch6-q10",
      difficulty: "core",
      concept: "balance-book",
      prompt:
        "A transaction posted today with a value date two weeks in the future immediately increases the account's book balance.",
      answer: true,
      explanation:
        "The [[balance-book]] (ledger balance) reflects every posted transaction ordered by booking date, regardless of when the value date falls. The value-date balance (interest-bearing balance) would not yet include this entry — so book balance and value-date balance temporarily diverge.",
    },
    {
      kind: "mc",
      id: "ch6-q11",
      difficulty: "core",
      concept: "balance-available",
      prompt:
        "An account starts at $1,000. A check for $200 is deposited and posted immediately (booking date today, value date in two days). The bank places a $200 hold on the deposit while it clears. What is the available balance?",
      options: [
        "$1,200 — the book balance after posting",
        "$1,000 — book balance minus the $200 hold",
        "$800 — deducting both the original balance and the hold",
        "$0 — the account is frozen until the value date",
      ],
      answer: 1,
      explanation:
        "[[balance-available]] = book balance − active holds = $1,200 − $200 = $1,000. Although the check already appears in the [[balance-book]], the hold prevents those funds from being spent until the check clears and the value date arrives.",
    },
    {
      kind: "numeric",
      id: "ch6-q12",
      difficulty: "core",
      concept: "balance-book",
      prompt:
        "An account holds $800. A check for $300 is deposited and immediately posted with today's booking date but a value date two business days away. What is the book balance in dollars immediately after posting?",
      answer: 1100,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The [[balance-book]] includes all posted transactions by booking date regardless of value date. The $300 check is posted, so book balance = $800 + $300 = **$1,100**. The value-date balance remains $800 until the value date arrives.",
    },
    {
      kind: "numeric",
      id: "ch6-q13",
      difficulty: "core",
      concept: "balance-available",
      prompt:
        "Same account: $800 starting balance, $300 check posted today and value-dated two business days away. The bank places a $300 hold on the deposit until clearance. What is the available balance in dollars?",
      answer: 800,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[balance-available]] = book balance − holds = $1,100 − $300 = **$800**. Even though the check raises the [[balance-book]], the hold matches the deposit amount exactly, leaving available funds unchanged until the value date passes and the hold is released.",
    },
    {
      kind: "mc",
      id: "ch6-q14",
      difficulty: "core",
      concept: "reversal",
      prompt:
        "A $500 credit was posted February 3 with a value date of February 3. On February 10 the bank discovers it was an error and posts a reversal. Which value date should the reversal carry to correctly unwind the interest effect?",
      options: [
        "February 10 — the date the reversal is booked",
        "February 3 — the original value date",
        "February 1 — the start of the relevant period",
        "No value date — reversals are purely accounting entries",
      ],
      answer: 1,
      explanation:
        "A [[reversal]] must mirror the original transaction's [[value-date]] (February 3) so that interest accrued from February 3 onward is correctly unwound. This follows directly from the core [[value-date]] principle: because interest accrual is governed entirely by value date, restoring the ledger to its pre-error state requires the reversal to carry the same value date as the original entry — not the date the error was discovered. Using February 10 as the value date would leave the account with incorrect interest for the intervening seven days.",
    },
    {
      kind: "multi",
      id: "ch6-q15",
      difficulty: "core",
      prompt:
        "Which of the following does the value date govern? Select all that apply.",
      options: [
        "When interest starts or stops accruing",
        "When funds become available to the account holder",
        "When the transaction appears in the bank's audit trail",
        "Which business day 'owns' the transaction for period-end balance calculations",
      ],
      answers: [0, 1, 3],
      explanation:
        "The [[value-date]] governs economic effect: interest accrual, fund availability, and balance-period assignment. Audit-trail ordering and operational reports follow the [[booking-date]], not the value date.",
    },
    {
      kind: "mc",
      id: "ch6-q16",
      difficulty: "challenge",
      concept: "statement",
      prompt:
        "A transaction is booked on February 25 with a value date of March 1. How does it appear in the customer's February bank statement?",
      options: [
        "Listed in February's transactions AND counted in February's closing balance",
        "Listed in February's transactions but NOT counted in February's closing balance",
        "Not listed in February's transactions but IS counted in February's closing balance",
        "Excluded from both February and March statements",
      ],
      answer: 1,
      explanation:
        "[[statement|Statements]] list transactions chronologically by [[booking-date]], so this entry appears in February's listing. But the closing balance is computed from [[value-date|value dates]] — with a value date of March 1, this transaction does not yet affect February's closing balance. This is why listed transactions on a statement may not appear to reconcile with the balance change shown.",
    },
    {
      kind: "mc",
      id: "ch6-q17",
      difficulty: "challenge",
      concept: "clearing-vs-settlement",
      prompt:
        "The booking-vs-value-date gap is described as 'precisely the window between initiating a payment and settling it.' In payment network terms, this gap corresponds to:",
      options: [
        "The difference between gross and net settlement models",
        "The interval between clearing (initiation) and final settlement",
        "The delay between hold authorization and hold capture",
        "The lag between a ledger posting and its reconciliation",
      ],
      answer: 1,
      explanation:
        "[[clearing-vs-settlement|Clearing]] is when the payment is initiated and booked (the [[booking-date]]); settlement is when funds finally and irrevocably move (the [[value-date]]). The two-date distinction introduced in this chapter is the same gap that governs how money moves between banks in the payment network chapters.",
    },
    {
      kind: "multi",
      id: "ch6-q18",
      difficulty: "challenge",
      prompt:
        "Which of the following parties can determine or constrain the value date of a transaction? Select all that apply.",
      options: [
        "The bank's automated rules engine",
        "Payment networks such as SWIFT",
        "The customer, for scheduled payments and standing orders",
        "The beneficiary (receiving) account holder",
      ],
      answers: [0, 1, 2],
      explanation:
        "The [[value-date]] is set by automated bank rules (for most transactions), by payment networks that carry a value-date field (e.g., SWIFT), and by the customer when choosing when a scheduled payment takes effect. Regulation (e.g., US Reg CC) bounds all of the above. The beneficiary's account holder has no role in determining the value date.",
    },
    {
      kind: "multi",
      id: "ch6-q19",
      difficulty: "challenge",
      concept: "snapshot",
      prompt:
        "Which of the following are true of end-of-day balance snapshots? Select all that apply.",
      options: [
        "They are calculated using value dates, not booking dates",
        "They form the basis for the bank's interest accrual calculations",
        "They include forward-value-dated transactions not yet in economic effect",
        "They determine the opening balance for the following statement period",
      ],
      answers: [0, 1, 3],
      explanation:
        "[[snapshot|End-of-day snapshots]] record an account's [[balance-book|book]], [[balance-holds|holds]], and [[balance-available|available]] balances on a **value-date** basis — the economically real position (option 0). That is why they are the authoritative basis for daily interest accrual (option 1) and supply the opening/closing figures for the next statement period (option 3). Option 2 is wrong: precisely because the snapshot uses the value-date balance, a forward-value-dated transaction is *excluded* until its value date arrives and it becomes economically effective.",
    },
    {
      kind: "numeric",
      id: "ch6-q20",
      difficulty: "challenge",
      concept: "statement-amount",
      prompt:
        "An account's value-date balance at the end of January is $500. Two transactions are then posted: a $200 credit posted January 31 with a value date of February 1, and a $100 credit posted February 25 with a value date of March 1. What is the account's February closing balance in dollars?",
      answer: 700,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The [[statement-amount|closing balance]] is computed from [[value-date|value dates]]. The $200 credit has value date February 1 — within February — raising the balance to $700. The $100 credit has value date March 1 — outside February — so it does not affect February's closing balance. Result: $500 + $200 = **$700**.",
    },
  ],
};
