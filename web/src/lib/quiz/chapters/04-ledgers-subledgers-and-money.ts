import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "04-ledgers-subledgers-and-money",
  number: 4,
  part: "Part I · The Foundations of Bank Accounting",
  title: "Ledgers, Subledgers, and Money",
  questions: [
    {
      kind: "mc",
      id: "ch4-q1",
      difficulty: "intro",
      concept: "ledger-vs-subledger",
      prompt: "What is the General Ledger (GL)?",
      options: [
        "A detailed log of every individual customer's transactions",
        "The top-level book of all accounts that holds summary balances",
        "A regulatory report filed with the central bank",
        "A subledger dedicated to interbank settlements",
      ],
      answer: 1,
      explanation:
        "The [[ledger-vs-subledger]] relationship: the **General Ledger** is the top-level summary book. It aggregates balances across all account types. Individual detail lives in subledgers that roll up into it.",
    },
    {
      kind: "mc",
      id: "ch4-q2",
      difficulty: "intro",
      concept: "ledger-vs-subledger",
      prompt: "A bank has 50,000 customer deposit accounts. Where does it store each individual account's balance?",
      options: [
        "Directly in the General Ledger, one row per customer",
        "In a Customer Deposits subledger, which summarises into one GL account",
        "In a separate database outside the accounting system",
        "In the equity section of the balance sheet",
      ],
      answer: 1,
      explanation:
        "Individual accounts live in the **Customer Deposits [[ledger-vs-subledger|subledger]]**. The GL only holds the aggregate balance for the 'Customer Deposits' control account — the detail is in the subledger.",
    },
    {
      kind: "truefalse",
      id: "ch4-q3",
      difficulty: "intro",
      concept: "amount-cents",
      prompt: "Monetary amounts should be stored as floating-point numbers (e.g., 100.50 as a float) to preserve decimal precision.",
      answer: false,
      explanation:
        "Floating-point cannot represent most decimal fractions exactly — `0.1 + 0.2` is not `0.30` in binary. [[amount-cents]]: store money as **integers in the smallest unit** (e.g., 10050 for $100.50).",
    },
    {
      kind: "numeric",
      id: "ch4-q4",
      difficulty: "intro",
      concept: "amount-cents",
      unit: "cents",
      prompt: "How many cents is $100.50? Enter your answer in cents.",
      answer: 10050,
      explanation:
        "[[amount-cents]]: $100.50 × 100 = **10050 cents**. Storing as an integer avoids all floating-point rounding errors.",
    },
    {
      kind: "numeric",
      id: "ch4-q5",
      difficulty: "core",
      concept: "amount-cents",
      unit: "cents",
      prompt: "A wire transfer is for $1,234.56. What integer value is stored in the ledger? Enter your answer in cents.",
      answer: 123456,
      explanation:
        "[[amount-cents]]: $1,234.56 × 100 = **123456 cents**. The accounting engine never works with decimal dollars — conversion to a human-readable format is the UI's responsibility.",
    },
    {
      kind: "mc",
      id: "ch4-q6",
      difficulty: "core",
      concept: "ledger-vs-subledger",
      prompt: "An auditor wants to verify a specific customer's transaction history. Where would she find this detail?",
      options: [
        "The General Ledger — it contains every transaction for every account",
        "The Customer Deposits subledger — it holds per-account transaction detail",
        "The income statement — it summarises all customer activity",
        "The trial balance — it lists all balances at a point in time",
      ],
      answer: 1,
      explanation:
        "The GL shows summary control-account balances. Per-customer transaction history is in the **[[ledger-vs-subledger|subledger]]** — that's its purpose: individual detail that sums to the GL control account.",
    },
    {
      kind: "truefalse",
      id: "ch4-q7",
      difficulty: "core",
      concept: "amount-cents",
      prompt:
        "A 64-bit integer of cents can safely represent balances in the trillions of dollars, making it suitable for any realistic banking amount.",
      answer: true,
      explanation:
        "A 64-bit signed integer can hold up to ~9.2 × 10¹⁸ — roughly 92 quadrillion [[amount-cents|cents]] (~92 quadrillion dollars). No realistic account balance comes close.",
    },
    {
      kind: "mc",
      id: "ch4-q8",
      difficulty: "core",
      concept: "ledger-vs-subledger",
      prompt:
        "In the [[ledger-vs-subledger]] hierarchy, what must always be true about a subledger's total and its corresponding GL control account?",
      options: [
        "The GL control account balance must equal the sum of all subledger balances",
        "The subledger total must be greater than the GL balance to allow for provisions",
        "They can differ temporarily during reconciliation windows",
        "The GL balance is always zero because only the subledger records real amounts",
      ],
      answer: 0,
      explanation:
        "The subledger total and the GL control account **must agree at all times**. Any discrepancy is a reconciliation error — a sign that a posting hit one but not the other.",
    },
    {
      kind: "mc",
      id: "ch4-q9",
      difficulty: "challenge",
      concept: "amount-cents",
      prompt:
        "A developer stores a payment amount as the floating-point value 0.1 + 0.2 and later compares it to 0.30. The comparison returns false. What is the root cause?",
      options: [
        "Floating-point cannot represent 0.1 or 0.2 exactly in binary, so their sum is not exactly 0.30",
        "The database rounded the value during storage",
        "JavaScript integers overflow at 0.30",
        "The comparison should use == instead of ===",
      ],
      answer: 0,
      explanation:
        "Binary floating-point has no exact representation for most decimal fractions. `0.1 + 0.2` evaluates to something like `0.30000000000000004`. This is exactly why [[amount-cents]] stores money as integers — the arithmetic is exact.",
    },
    {
      kind: "multi",
      id: "ch4-q10",
      difficulty: "challenge",
      concept: "ledger-vs-subledger",
      prompt:
        "Which statements correctly describe the relationship between the General Ledger and subledgers? (Select all that apply.)",
      options: [
        "The GL holds aggregate summary balances; subledgers hold per-entity detail",
        "A subledger's total must reconcile to its GL control account",
        "The GL stores more granular data than subledgers",
        "Subledgers are optional convenience views with no accounting significance",
        "Customer deposit accounts live in a subledger, not directly in the GL",
      ],
      answers: [0, 1, 4],
      explanation:
        "The [[ledger-vs-subledger]] hierarchy is fundamental: the GL summarises, subledgers detail. Reconciliation between them is mandatory — it is how the bank proves its books are complete. The GL has less granularity, not more, and subledgers are core accounting structures.",
    },
  ],
};
