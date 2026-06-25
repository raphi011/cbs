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
      prompt: "What is the book balance of an account?",
      options: [
        "The balance shown to the customer in real time, including holds",
        "The sum of all posted transactions up to and including the booking date",
        "The balance after subtracting any active holds",
        "The balance calculated using only value-dated transactions",
      ],
      answer: 1,
      explanation:
        "The [[balance-book]] is derived from posted ledger entries ordered by booking date. It does not reflect holds (which are off-ledger) or the economic timing of the [[value-date]].",
    },
    {
      kind: "mc",
      id: "ch7-q2",
      difficulty: "intro",
      concept: "balance-available",
      prompt:
        "A customer's account has a book balance of $500. There is one active hold for $120. What is the available balance (assume no overdraft facility)?",
      options: ["$620", "$500", "$380", "$120"],
      answer: 2,
      explanation:
        "[[balance-available]] = Book balance − Active holds = $500 − $120 = $380. The [[holds|hold]] is not a ledger entry, but it still reduces the amount the customer can spend.",
    },
    {
      kind: "truefalse",
      id: "ch7-q3",
      difficulty: "intro",
      concept: "holds",
      prompt:
        "Placing a hold on a customer's account creates a debit ledger entry immediately.",
      answer: false,
      explanation:
        "A [[holds|hold]] is tracked *outside* the ledger — it is a reservation, not a posting. The ledger is only affected at [[hold-capture|capture]] time. If the hold is [[hold-release|released]], nothing ever posts to the ledger.",
    },
    {
      kind: "mc",
      id: "ch7-q4",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "A customer pays $45 at a gas station after a $100 authorization hold was placed. What happens at capture?",
      options: [
        "A $100 debit posts to the ledger; the $55 difference is refunded separately",
        "A $45 debit posts to the ledger; the $100 hold is fully released",
        "A $45 debit posts to the ledger; the remaining $55 of the hold is released",
        "No ledger entry is created — holds never reach the ledger",
      ],
      answer: 2,
      explanation:
        "At [[hold-capture|capture]], only the actual amount ($45) posts to the ledger as a debit entry. The original $100 hold is replaced: $45 captured and $55 [[hold-release|released]] back to available balance. The ledger never saw the full $100.",
    },
    {
      kind: "mc",
      id: "ch7-q5",
      difficulty: "core",
      concept: "holds",
      prompt:
        "A $200 hotel pre-authorization hold expires without being captured. What is the net effect on the ledger?",
      options: [
        "A $200 debit and a $200 credit are posted, netting to zero",
        "A $200 credit is posted to reverse the original hold",
        "No ledger entries are created — holds never reach the ledger",
        "A $200 debit remains on the ledger permanently",
      ],
      answer: 2,
      explanation:
        "Since [[holds]] are off-ledger reservations, a [[hold-release|release]] (including expiry) does not create any ledger entries. The hold simply stops reducing the [[balance-available|available balance]]. The [[balance-book]] was never affected.",
    },
    {
      kind: "mc",
      id: "ch7-q6",
      difficulty: "core",
      concept: "balance-holds",
      prompt:
        "Which three components make up a complete picture of an account's balance?",
      options: [
        "Book balance, credit limit, and overdraft allowance",
        "Book balance, value-date balance, and available balance",
        "Debit total, credit total, and net position",
        "Opening balance, daily credits, and daily debits",
      ],
      answer: 1,
      explanation:
        "Banking systems typically track three distinct balances: [[balance-book]] (posted entries by booking date), value-date balance (entries whose [[value-date]] has passed), and [[balance-available]] (book balance minus active [[holds]]).",
    },
    {
      kind: "truefalse",
      id: "ch7-q7",
      difficulty: "core",
      concept: "balance-available",
      prompt:
        "The available balance can be higher than the book balance if the customer has an arranged overdraft facility.",
      answer: true,
      explanation:
        "[[balance-available]] = Book balance − Active holds + Overdraft limit. An arranged overdraft effectively adds to spendable capacity, so the available balance can exceed the [[balance-book]].",
    },
    {
      kind: "multi",
      id: "ch7-q8",
      difficulty: "core",
      concept: "holds",
      prompt:
        "Which of the following statements about holds are correct? (Select all that apply.)",
      options: [
        "A hold reduces the available balance immediately",
        "A hold creates a debit entry in the ledger at authorization time",
        "A hold can expire without ever reaching the ledger",
        "At capture, the exact authorized amount always posts to the ledger",
        "Releasing a hold has no effect on any ledger entries",
      ],
      answers: [0, 2, 4],
      explanation:
        "[[holds]] are off-ledger reservations. They reduce [[balance-available]] immediately (true), but never create ledger entries at authorization (false). They can expire without posting (true). At [[hold-capture|capture]], only the *actual* amount posts — not necessarily the full authorized amount (false). [[hold-release|Releasing]] a hold simply removes the reservation; no ledger entry is created (true).",
    },
    {
      kind: "numeric",
      id: "ch7-q9",
      difficulty: "challenge",
      concept: "balance-available",
      unit: "dollars",
      prompt:
        "An account has a book balance of $1,000, two active holds of $150 and $75, and an arranged overdraft limit of $200. What is the available balance in dollars?",
      answer: 975,
      explanation:
        "[[balance-available]] = Book balance − Active holds + Overdraft limit = $1,000 − $150 − $75 + $200 = $975. The overdraft limit adds to spendable capacity while [[holds]] reduce it.",
    },
    {
      kind: "mc",
      id: "ch7-q10",
      difficulty: "challenge",
      concept: "balance-book",
      prompt:
        "A customer sees a book balance of $300 but cannot spend more than $180. Which explanation is most likely?",
      options: [
        "The value-date balance is $180",
        "There is a $120 active hold reducing the available balance",
        "The account has a $180 credit limit",
        "The bank has applied a $120 fee to the account",
      ],
      answer: 1,
      explanation:
        "[[balance-available]] = $300 − $120 hold = $180. The [[balance-book]] is unaffected by [[holds]] because holds are off-ledger, but they directly reduce the [[balance-available|available balance]] that governs what the customer can spend.",
    },
  ],
};
