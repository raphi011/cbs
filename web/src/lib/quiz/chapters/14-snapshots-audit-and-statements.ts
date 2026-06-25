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
      prompt: "What does an end-of-day snapshot capture for each account?",
      options: [
        "Only the book balance at midnight",
        "Book balance, holds, and available balance at close of business",
        "The full transaction history since account opening",
        "The customer's credit score and risk rating",
      ],
      answer: 1,
      explanation:
        "A [[snapshot]] records three values per account at end-of-day: **book balance** (all settled postings), **holds** (pending authorizations), and **available balance** (book minus holds). This point-in-time record drives interest accrual, regulation, and statements.",
    },
    {
      kind: "multi",
      id: "ch14-q2",
      difficulty: "intro",
      concept: "snapshot",
      prompt: "Select ALL purposes served by end-of-day snapshots.",
      options: [
        "Daily interest accrual (principal × rate / 365)",
        "Customer statement generation",
        "Regulatory reporting",
        "Performance — checkpoint for balance queries",
        "Blocking new payments until the snapshot is processed",
      ],
      answers: [0, 1, 2, 3],
      explanation:
        "[[snapshot]] records serve four purposes: interest accrual, statement generation, regulatory reporting, and query performance (balance at a historical date can be read from the snapshot rather than replaying all events). Snapshots do not block payment processing.",
    },
    {
      kind: "truefalse",
      id: "ch14-q3",
      difficulty: "intro",
      concept: "snapshot",
      prompt:
        "A snapshot uses the booking-date balance (when transactions were recorded) rather than the value-date balance.",
      answer: false,
      explanation:
        "Snapshots capture the **value-date** balance — the economically real position. Interest and regulation use value-date balances because value date is when money is economically available, not merely when the entry was logged.",
    },
    {
      kind: "mc",
      id: "ch14-q4",
      difficulty: "core",
      concept: "statement",
      prompt:
        "A customer statement transaction listing is ordered by which date?",
      options: [
        "Value date (when money is economically effective)",
        "Booking date (when the transaction was recorded in the ledger)",
        "Settlement date (when reserves moved)",
        "Creation date of the account",
      ],
      answer: 1,
      explanation:
        "The [[statement]] transaction listing uses **booking date** — the date the entry was recorded. Opening and closing balances, however, use value date. This is by design and may cause a visible mismatch between the listing and the balance change.",
    },
    {
      kind: "truefalse",
      id: "ch14-q5",
      difficulty: "core",
      concept: "statement-amount",
      prompt:
        "It is incorrect for a transaction booked in February with a value date in March to appear in the February statement's transaction listing but NOT affect the February closing balance.",
      answer: false,
      explanation:
        "This is **correct behavior**. The [[statement-amount]] closing balance uses value-date snapshots, so a transaction booked Feb 25 with value date March 1 appears in the February listing (booked then) but does not affect February's closing balance (economically effective in March).",
    },
    {
      kind: "mc",
      id: "ch14-q6",
      difficulty: "core",
      concept: "statement",
      prompt:
        "Daily balance figures on a customer statement are derived from which source?",
      options: [
        "Recomputing all postings from scratch each time the statement is generated",
        "End-of-day snapshots (value-date based)",
        "The booking-date running total from the transaction listing",
        "A separate customer-facing ledger maintained by the statement engine",
      ],
      answer: 1,
      explanation:
        "Daily balances on a [[statement]] come from [[snapshot]] records — value-date end-of-day balances captured at close of business. This makes statement generation fast and consistent without replaying the entire event log.",
    },
    {
      kind: "mc",
      id: "ch14-q7",
      difficulty: "core",
      concept: "booking-date",
      prompt:
        "An account has a balance of $1,000 and earns 3.65% annual interest. Using the snapshot model (interest = principal × rate / 365), how many cents of interest accrue on this balance for one day?",
      options: [
        "10 cents",
        "100 cents ($1.00)",
        "36 cents",
        "3 cents",
      ],
      answer: 0,
      explanation:
        "Daily interest = $1,000 × 0.0365 / 365 = $1,000 × 0.0001 = **$0.10 (10 cents)**. The [[snapshot]] provides the principal for each day's calculation.",
    },
    {
      kind: "mc",
      id: "ch14-q8",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "An auditor wants to verify that the current account balance is correct. What is the strongest correctness check available?",
      options: [
        "Read the latest snapshot and compare it to the current balance",
        "Replay all events in the append-only audit log from the beginning and recompute the balance from scratch",
        "Ask the customer to confirm their balance",
        "Compare the book balance to the available balance",
      ],
      answer: 1,
      explanation:
        "Recomputing balances by replaying the immutable [[reversal]]-friendly append-only audit log from scratch is the **strongest form of correctness check** — it independently derives what the balance must be from first principles, without trusting any cached or snapshotted figure.",
    },
    {
      kind: "multi",
      id: "ch14-q9",
      difficulty: "challenge",
      concept: "statement",
      prompt:
        "Select ALL types of events that appear in an immutable audit trail.",
      options: [
        "Account opening",
        "Every posting (debit or credit)",
        "Hold placements and releases",
        "Reversals",
        "Snapshot captures",
        "Customer login events",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "The [[statement]] audit trail logs every ledger event: account lifecycle (opening), all postings, holds and releases, reversals, and snapshots. Customer authentication events are handled by identity/access systems, not the ledger audit trail.",
    },
    {
      kind: "truefalse",
      id: "ch14-q10",
      difficulty: "challenge",
      concept: "value-date",
      prompt:
        "Modifying a historical posting is an acceptable way to correct a mistake, since the audit trail will still show the final correct state.",
      answer: false,
      explanation:
        "The audit trail is **immutable and append-only**. Corrections are made by posting a new [[reversal]] (compensating entry) that undoes the original, followed by the correct entry. Modifying history destroys the audit trail's value for forensics, compliance, and independent verification.",
    },
  ],
};
