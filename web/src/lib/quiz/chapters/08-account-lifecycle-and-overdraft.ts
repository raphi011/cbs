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
      prompt: "Which account status permits all operations — debits, credits, and new holds?",
      options: ["Dormant", "Frozen", "Active", "Closed"],
      answer: 2,
      explanation:
        "An [[account-status|Active]] account is the fully operational state: it accepts debits, credits, and hold authorizations. [[account-status|Dormant]] restricts to credits only; [[account-status|Frozen]] allows viewing only; [[account-status|Closed]] permits nothing.",
    },
    {
      kind: "mc",
      id: "ch8-q2",
      difficulty: "intro",
      concept: "account-status",
      prompt: "What operation can still be performed on a Dormant account?",
      options: [
        "Placing a hold",
        "Initiating a debit payment",
        "Receiving an incoming credit",
        "Closing the account while it has a balance",
      ],
      answer: 2,
      explanation:
        "A [[account-status|Dormant]] account accepts incoming credits. This allows salary payments or refunds to reach the account. The dormant state is typically triggered by inactivity, and any incoming credit (or customer activity) can restore it to Active.",
    },
    {
      kind: "truefalse",
      id: "ch8-q3",
      difficulty: "intro",
      concept: "account-status",
      prompt: "A Closed account can be reopened if the customer returns to the bank.",
      answer: false,
      explanation:
        "[[account-status|Closed]] is a terminal state — it can never be reopened. A new account must be created. To reach Closed, the balance must be exactly zero and all holds resolved.",
    },
    {
      kind: "mc",
      id: "ch8-q4",
      difficulty: "core",
      concept: "account-status",
      prompt:
        "A fraud alert is raised on an Active account. The account is Frozen. The alert is later cleared. What state should the account return to?",
      options: [
        "Closed, because it was flagged",
        "Dormant, as a precaution",
        "Active, because that was its state before freezing",
        "It remains Frozen until the customer explicitly requests reactivation",
      ],
      answer: 2,
      explanation:
        "The [[account-status|Frozen]] state remembers the prior state. When the freeze is lifted, the account returns to Active (its previous state). If it had been Dormant before being Frozen, it would return to Dormant.",
    },
    {
      kind: "multi",
      id: "ch8-q5",
      difficulty: "core",
      concept: "account-status",
      prompt:
        "Which conditions must be met before an Active account can be Closed? (Select all that apply.)",
      options: [
        "The book balance must be exactly zero",
        "All active holds must be resolved",
        "The customer must have been Active for at least 30 days",
        "No pending incoming payments are expected",
        "The account must first pass through Dormant",
      ],
      answers: [0, 1],
      explanation:
        "Closing an account requires the [[balance-book]] to be exactly zero and all [[holds]] to be resolved. There is no minimum age requirement and no mandatory Dormant step. [[account-status|Closed]] is reached directly from Active when these financial conditions are satisfied.",
    },
    {
      kind: "mc",
      id: "ch8-q6",
      difficulty: "core",
      concept: "overdraft",
      prompt:
        "A customer with no overdraft facility tries to debit $600 from an account with a $500 available balance. What happens?",
      options: [
        "The transaction posts and the book balance goes to −$100",
        "The transaction is hard-declined before posting",
        "A hold is placed for $600 and the transaction is queued",
        "The transaction posts for $500 and the remainder is rejected",
      ],
      answer: 1,
      explanation:
        "Without an arranged [[overdraft]], a hard decline rejects the transaction before it ever reaches the ledger. The overdraft limit is a business-rule gate checked against the [[balance-available]] *before* posting.",
    },
    {
      kind: "truefalse",
      id: "ch8-q7",
      difficulty: "core",
      concept: "overdraft",
      prompt:
        "With an arranged overdraft, the book balance can go negative.",
      answer: true,
      explanation:
        "An arranged [[overdraft]] explicitly allows the [[balance-book]] to go negative up to the agreed limit — the customer owes the bank. The overdraft limit is enforced *before* posting (against available balance), but once a transaction is approved within the limit, the resulting book balance can be negative.",
    },
    {
      kind: "numeric",
      id: "ch8-q8",
      difficulty: "core",
      concept: "overdraft",
      unit: "dollars",
      prompt:
        "An account has a book balance of $50, an arranged overdraft limit of $200, and one active hold of $30. What is the available balance in dollars?",
      answer: 220,
      explanation:
        "[[balance-available]] = Book balance + [[overdraft|Overdraft limit]] − Active holds = $50 + $200 − $30 = $220. The overdraft limit adds to spendable capacity; the [[holds|hold]] reduces it.",
    },
    {
      kind: "mc",
      id: "ch8-q9",
      difficulty: "challenge",
      concept: "overdraft",
      prompt:
        "The overdraft limit is checked against which balance before a debit is approved?",
      options: [
        "The book balance only",
        "The value-date balance",
        "The available balance (book balance + overdraft limit − active holds)",
        "The closing balance from the previous business day",
      ],
      answer: 2,
      explanation:
        "The [[overdraft]] gate compares the proposed debit against the [[balance-available]] — which already incorporates the overdraft limit and subtracts active [[holds]]. Using only the book balance would ignore reserved funds and available credit.",
    },
    {
      kind: "mc",
      id: "ch8-q10",
      difficulty: "challenge",
      concept: "account-status",
      prompt:
        "An account transitions: Active → Dormant → Frozen → ? (freeze lifted). What is the resulting state?",
      options: ["Active", "Dormant", "Closed", "Frozen"],
      answer: 1,
      explanation:
        "[[account-status|Frozen]] remembers the state immediately before freezing. Because the account was Dormant when it was Frozen, lifting the freeze returns it to Dormant — not Active. Only a qualifying customer action or incoming credit would then move it back to Active.",
    },
  ],
};
