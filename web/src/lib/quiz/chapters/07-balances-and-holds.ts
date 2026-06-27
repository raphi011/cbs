import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "07-balances-and-holds",
  number: 7,
  part: "Part II · Transactions and Time",
  title: "Balances and Holds",
  questions: [
    {
      kind: "mc",
      id: "ch7-q1",
      difficulty: "intro",
      concept: "balance-book",
      prompt:
        "What is the book balance of an account (also called the ledger balance)?",
      options: [
        "The amount the customer can spend right now, after subtracting holds",
        "The sum of all posted transactions ordered by booking date, regardless of value date",
        "The sum of transactions whose value date has already passed",
        "The book balance minus any active holds",
      ],
      answer: 1,
      explanation:
        "The [[balance-book]] is derived from every posted ledger entry ordered by booking date. It does not reflect [[holds]] (which are off-ledger) nor the economic timing captured by the [[value-date]].",
    },
    {
      kind: "truefalse",
      id: "ch7-q2",
      difficulty: "intro",
      concept: "holds",
      prompt:
        "Placing a hold on an account immediately creates a debit entry in the ledger.",
      answer: false,
      explanation:
        "A [[holds|hold]] is an off-ledger reservation — it is tracked alongside the ledger, not within it. The ledger is only touched at [[hold-capture|capture]] time. If the hold is [[hold-release|released]], nothing ever posts to the ledger.",
    },
    {
      kind: "mc",
      id: "ch7-q3",
      difficulty: "intro",
      prompt:
        "Which formula correctly expresses the available balance, ignoring any overdraft facility?",
      options: [
        "Book balance + Active holds",
        "Value-date balance − Active holds",
        "Book balance − Active holds",
        "Book balance − Value-date balance",
      ],
      answer: 2,
      explanation:
        "[[balance-available]] = Book balance − Active holds. [[holds|Holds]] are off-ledger reservations that reduce what the customer can spend without changing the [[balance-book|book balance]]. An overdraft limit, covered in the next chapter, would add to this figure.",
    },
    {
      kind: "numeric",
      id: "ch7-q4",
      difficulty: "intro",
      concept: "balance-available",
      prompt:
        "An account has a book balance of $500 and one active hold of $120. What is the available balance in dollars?",
      answer: 380,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[balance-available]] = Book balance − Active holds = $500 − $120 = **$380**. The [[holds|hold]] is an off-ledger reservation: it reduces spendable funds without changing the [[balance-book|book balance]].",
    },
    {
      kind: "mc",
      id: "ch7-q5",
      difficulty: "intro",
      concept: "value-date",
      prompt:
        "What is the value-date balance (also called the interest-bearing balance) primarily used for?",
      options: [
        "Approving or declining card payments at the point of sale",
        "Calculating interest and generating end-of-day regulatory snapshots",
        "Tracking the total amount currently reserved in active holds",
        "Determining how much of the book balance has been captured from holds",
      ],
      answer: 1,
      explanation:
        "The [[value-date]] balance includes only transactions whose value date has passed — the economic reality of the account. The bank uses it to calculate interest, generate end-of-day [[snapshot|snapshots]], and produce regulatory reports. Card approvals check the [[balance-available|available balance]] instead.",
    },
    {
      kind: "mc",
      id: "ch7-q6",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "A customer swipes their card at a gas station. A $100 authorization hold is placed, but they pump only $45 of fuel. At capture, what happens?",
      options: [
        "A $100 debit posts to the ledger; the $55 excess is separately refunded",
        "A $45 debit posts to the ledger and the remaining $55 of the hold is released",
        "No ledger entry is created because the hold already reserved the funds",
        "A $45 debit posts; the original $100 hold remains active until it expires",
      ],
      answer: 1,
      explanation:
        "At [[hold-capture|capture]], only the actual amount ($45) posts to the ledger as a real, balanced transaction. The $100 reservation is replaced: $45 is captured and $55 is [[hold-release|released]] back to the [[balance-available|available balance]]. The ledger never saw the full $100.",
    },
    {
      kind: "multi",
      id: "ch7-q7",
      difficulty: "core",
      concept: "holds",
      prompt:
        "A $200 hotel pre-authorization hold expires without ever being captured. Which of the following are true? (Select all that apply.)",
      options: [
        "The book balance is unchanged throughout the hold's entire lifetime",
        "A $200 credit is posted to the ledger to cancel the original hold debit",
        "The available balance returns to its pre-hold value when the hold expires",
        "The expired hold appears as a pending entry on the account statement",
        "No ledger entries are created at any point in this scenario",
      ],
      answers: [0, 2, 4],
      explanation:
        "[[holds|Holds]] are off-ledger reservations. They never touch the [[balance-book|book balance]] (true). An expiry is equivalent to a [[hold-release|release]] — no ledger entry is ever created (true). The [[balance-available|available balance]] is restored automatically (true). Because nothing reached the ledger, no statement entry is produced (false for option 3).",
    },
    {
      kind: "mc",
      id: "ch7-q8",
      difficulty: "core",
      concept: "balance-available",
      prompt:
        "When an ATM or point-of-sale terminal decides whether to approve a transaction, which balance does it check?",
      options: [
        "Book balance — the total of all posted transactions",
        "Value-date balance — the economically settled balance",
        "Available balance — the book balance minus active holds",
        "The total of active holds alone",
      ],
      answer: 2,
      explanation:
        "The [[balance-available|available balance]] is the money a customer can actually spend right now. Card systems check this figure, not the [[balance-book|book balance]], which could look larger because it ignores the funds reserved by [[holds]].",
    },
    {
      kind: "truefalse",
      id: "ch7-q9",
      difficulty: "core",
      concept: "balance-book",
      prompt: "Placing a hold on an account reduces the book (ledger) balance immediately.",
      answer: false,
      explanation:
        "The [[balance-book]] is computed from posted ledger entries only. Because a [[holds|hold]] is an off-ledger reservation — not a posting — the book balance is completely unaffected when a hold is placed. Only the [[balance-available|available balance]] drops.",
    },
    {
      kind: "mc",
      id: "ch7-q10",
      difficulty: "core",
      concept: "balance-holds",
      prompt:
        "An account simultaneously shows three different balance figures: $9,500, $10,000, and $9,200. According to the chapter, which assignment is correct?",
      options: [
        "Book = $9,500 · Value-date = $10,000 · Available = $9,200",
        "Book = $10,000 · Value-date = $9,500 · Available = $9,200",
        "Book = $9,200 · Value-date = $9,500 · Available = $10,000",
        "Book = $10,000 · Available = $9,500 · Value-date = $9,200",
      ],
      answer: 1,
      explanation:
        "The [[balance-book]] ($10,000) reflects all posted entries. The [[value-date]] balance ($9,500) is lower because a forward-dated transaction has been booked but hasn't reached its value date yet. The [[balance-available]] ($9,200) is lower still because $800 of active [[holds]] are reducing spendable funds.",
    },
    {
      kind: "multi",
      id: "ch7-q11",
      difficulty: "core",
      concept: "holds",
      prompt:
        "Which of the following statements about holds are correct? (Select all that apply.)",
      options: [
        "A hold reduces the available balance immediately upon placement",
        "A hold creates a debit entry in the ledger at the moment of authorization",
        "A hold can expire without any ledger entry ever being created",
        "At capture, the exact authorized amount always posts — not the actual transaction amount",
        "Releasing a hold restores the available balance without creating any ledger entry",
      ],
      answers: [0, 2, 4],
      explanation:
        "[[holds|Holds]] are off-ledger reservations. They reduce [[balance-available]] immediately (true) but never create ledger entries at authorization (false). They can expire with no ledger trace (true). At [[hold-capture|capture]], only the *actual* amount posts — not necessarily the full authorization (false). [[hold-release|Releasing]] a hold simply removes the reservation; no ledger entry is created (true).",
    },
    {
      kind: "truefalse",
      id: "ch7-q12",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "When a hold is captured, the full authorized amount always posts to the ledger.",
      answer: false,
      explanation:
        "At [[hold-capture|capture]], only the *actual* transaction amount posts. In the gas station example, a $100 authorization hold is captured for $45 — only $45 reaches the ledger. The difference is [[hold-release|released]] back to the [[balance-available|available balance]].",
    },
    {
      kind: "numeric",
      id: "ch7-q13",
      difficulty: "core",
      concept: "balance-holds",
      prompt:
        "An account's book balance is $10,000 and its available balance is $9,200. No overdraft facility is in place. What is the total dollar amount currently tied up in active holds?",
      answer: 800,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Rearranging the formula: Active holds = [[balance-book|Book balance]] − [[balance-available|Available balance]] = $10,000 − $9,200 = **$800**. This matches the chapter's worked example exactly.",
    },
    {
      kind: "mc",
      id: "ch7-q14",
      difficulty: "core",
      concept: "hold-release",
      prompt:
        "What happens when an outstanding hold is released — for example, because a sale was cancelled?",
      options: [
        "A reversal entry is posted to the ledger to undo the original authorization hold",
        "The book balance is restored to its value before the hold was placed",
        "The available balance is restored; no ledger entry is created",
        "A credit is posted and the released hold appears on the customer's statement",
      ],
      answer: 2,
      explanation:
        "A [[hold-release|release]] simply removes the off-ledger reservation. Because [[holds]] never created a ledger entry in the first place, there is nothing to reverse. The [[balance-available|available balance]] is restored and the [[balance-book|book balance]] is unchanged throughout.",
    },
    {
      kind: "mc",
      id: "ch7-q15",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "When a $60 hold is captured, which ledger entries are posted?",
      options: [
        "Debit $60 to a holds account; credit $60 to the customer's deposit account",
        "Debit $60 to the customer's deposit liability; credit $60 to the counterparty",
        "Credit $60 to the customer's deposit account — no debit is needed since the hold pre-reserved the funds",
        "No entries are posted; holds are always settled off-ledger",
      ],
      answer: 1,
      explanation:
        "At [[hold-capture|capture]], a real [[double-entry]] transaction posts: the customer's deposit [[account-type-liability|liability]] is debited (reduced) and the counterparty is credited — the same balanced structure as any other posted transaction. The hold itself never created a ledger entry; only the capture does.",
    },
    {
      kind: "numeric",
      id: "ch7-q16",
      difficulty: "challenge",
      concept: "overdraft",
      prompt:
        "An account has a book balance of $1,000, two active holds of $150 and $75, and an arranged overdraft limit of $200. What is the available balance in dollars?",
      answer: 975,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[balance-available]] = Book balance − Active holds + [[overdraft|Overdraft limit]] = $1,000 − $150 − $75 + $200 = **$975**. An overdraft limit adds to spendable capacity while [[holds]] reduce it. Without the overdraft, available would be only $775.",
    },
    {
      kind: "mc",
      id: "ch7-q17",
      difficulty: "challenge",
      concept: "balance-available",
      prompt:
        "A customer's book balance is $1,050 and two active holds total $250, giving an available balance of $800. A merchant attempts to capture $900. What is the outcome?",
      options: [
        "Approved — the book balance of $1,050 is sufficient to cover $900",
        "Declined — the available balance of $800 is below the requested $900",
        "Approved — the $250 hold automatically converts to a credit covering the shortfall",
        "Partially approved for $800; the remaining $100 is separately declined",
      ],
      answer: 1,
      explanation:
        "Payment authorization checks the [[balance-available|available balance]], not the [[balance-book|book balance]]. $900 > $800 available, so the transaction is declined even though the book balance looks sufficient. The [[holds]] of $250 are still live reservations, not spendable funds.",
    },
    {
      kind: "multi",
      id: "ch7-q18",
      difficulty: "challenge",
      concept: "hold-capture",
      prompt:
        "A $100 authorization hold is placed on an account, then captured for $75. Which of the following correctly describe the outcome? (Select all that apply.)",
      options: [
        "A $75 debit posts to the ledger as a real, balanced transaction",
        "A $100 debit posts because that was the original authorized reservation",
        "The remaining $25 of the hold is released back to the available balance",
        "The hold lifecycle touches the ledger exactly once — at capture",
        "Two separate ledger entries are created: a $75 capture and a $25 reversal",
      ],
      answers: [0, 2, 3],
      explanation:
        "At [[hold-capture|capture]], only the actual amount ($75) posts as a real ledger entry — not the authorized $100 (false for option 1). The remaining $25 is [[hold-release|released]] back to the [[balance-available|available balance]] (true). The hold itself never created a ledger entry, so the ledger is touched exactly once (true). There is no separate reversal entry for the $25 (false for option 4).",
    },
    {
      kind: "mc",
      id: "ch7-q19",
      difficulty: "challenge",
      concept: "balance-holds",
      prompt:
        "The chapter describes holds as 'off-ledger' reservations. What is the most significant consequence of this design for accounting integrity?",
      options: [
        "Holds cannot be placed for amounts over $100 without a separate approval process",
        "The trial balance (debits = credits) remains intact no matter how many holds are active, because holds never introduce unbalanced entries",
        "Holds reduce both the book balance and the available balance by the same amount",
        "The value-date balance must be adjusted downward to reflect all outstanding holds",
      ],
      answer: 1,
      explanation:
        "Because [[holds]] never create ledger entries, they can never disturb the debit-credit equality that the [[balance-book]] depends on. The ledger remains a provably consistent record of settled value. The operational world of authorizations and reservations lives one layer above, in the deposit layer, without ever corrupting the books.",
    },
    {
      kind: "truefalse",
      id: "ch7-q20",
      difficulty: "challenge",
      concept: "hold-release",
      prompt:
        "A hold that expires or is released without capture will appear on the customer's account statement as a pending charge that was subsequently reversed.",
      answer: false,
      explanation:
        "Because [[holds]] are off-ledger, a [[hold-release|release]] leaves absolutely no trace in the ledger or on any statement. From an accounting standpoint, it is as though the hold never happened. Only [[hold-capture|captured]] holds — which post real transactions — ever appear on statements.",
    },
  ],
};
