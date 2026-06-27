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
      prompt: "What is the General Ledger (GL) in a bank?",
      options: [
        "A detailed transaction log for each individual customer account",
        "The top-level book of record that holds all of the bank's accounts at summary level",
        "A regulatory report submitted to the central bank each month",
        "A subledger dedicated to interbank and settlement accounts",
      ],
      answer: 1,
      explanation:
        "The [[ledger-vs-subledger|General Ledger]] is the top-level book of record. It aggregates balances via control accounts; per-customer detail lives in subledgers that roll up into it.",
    },
    {
      kind: "mc",
      id: "ch4-q2",
      difficulty: "intro",
      concept: "account-type-liability",
      prompt:
        "A bank's Customer Deposits subledger contains 50,000 checking accounts. What account type are these individual accounts?",
      options: [
        "Asset — the bank owns the deposited cash",
        "Equity — each account represents a share in the bank",
        "Liability — the bank owes the deposited balance back to each customer",
        "Revenue — deposits generate ongoing income",
      ],
      answer: 2,
      explanation:
        "Customer deposit accounts are [[account-type-liability|liabilities]]: the bank owes the deposited amount back on demand. They sit in the Customer Deposits subledger, grouped under a GL liability control account.",
    },
    {
      kind: "truefalse",
      id: "ch4-q3",
      difficulty: "intro",
      concept: "amount-cents",
      prompt:
        "Storing monetary amounts as floating-point numbers (e.g., `100.50` as a `float64`) is a safe and industry-standard approach for financial systems.",
      answer: false,
      explanation:
        "Floating-point cannot represent most decimal fractions exactly — `0.1 + 0.2` is not `0.30` in binary. The [[amount-cents|industry standard]] stores money as integers of the smallest currency unit (cents for USD), making all arithmetic exact.",
    },
    {
      kind: "numeric",
      id: "ch4-q4",
      difficulty: "intro",
      concept: "amount-cents",
      prompt: "A balance is stored internally as the integer 1234 cents. What is this amount in dollars? (Enter a number of dollars.)",
      answer: 12.34,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[amount-cents]]: banks store money as integer cents, so 1234 ÷ 100 = **$12.34**. Keeping the canonical value as the integer 1234 eliminates any floating-point rounding error.",
    },
    {
      kind: "mc",
      id: "ch4-q5",
      difficulty: "intro",
      concept: "ledger-vs-subledger",
      prompt:
        "What is the primary purpose of a subledger in a bank's accounting system?",
      options: [
        "To bypass double-entry rules for high-volume retail accounts",
        "To group related accounts and hold their per-entity detail, summarizing into a GL control account",
        "To produce independent regulatory filings, separate from the GL",
        "To store only liability accounts, keeping them separate from assets",
      ],
      answer: 1,
      explanation:
        "A [[ledger-vs-subledger|subledger]] groups related accounts (e.g., all customer deposits) and holds per-account detail. The GL sees only the aggregate total via a control account — the same data, two levels of zoom.",
    },
    {
      kind: "mc",
      id: "ch4-q6",
      difficulty: "core",
      concept: "amount-cents",
      prompt:
        "A wire transfer is for $1,234.56. Which stored integer correctly represents this amount using the integer-cents approach?",
      options: [
        "1234 — truncate the fractional cents",
        "123456",
        "12345.6 — preserve six significant digits",
        "123457 — round up to the nearest cent",
      ],
      answer: 1,
      explanation:
        "[[amount-cents]]: $1,234.56 × 100 = **123,456 cents**. The accounting engine stores this as the integer 123456 — no rounding, no decimal point, exact.",
    },
    {
      kind: "truefalse",
      id: "ch4-q7",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "The subledger hierarchy changes double-entry accounting: postings within a subledger do not need to balance, because the GL handles overall balancing at the summary level.",
      answer: false,
      explanation:
        "[[double-entry]] always applies regardless of level. Every posting — in a subledger or directly in the GL — must have equal debits and credits. The subledger structure is purely organizational and does not relax any accounting rule.",
    },
    {
      kind: "mc",
      id: "ch4-q8",
      difficulty: "core",
      concept: "ledger-vs-subledger",
      prompt:
        "What invariant must hold between a subledger and its corresponding GL control account?",
      options: [
        "The GL control account balance must exactly equal the sum of all individual subledger account balances",
        "The subledger total may exceed the GL control account balance to allow for provisioning",
        "They may differ temporarily during a reconciliation window — the GL is corrected at month-end",
        "The GL control account is always zero; only the subledger records actual money",
      ],
      answer: 0,
      explanation:
        "The [[ledger-vs-subledger|subledger-to-GL reconciliation]] must hold at all times. Any mismatch is a ledger error — a posting reached one level but not the other — requiring immediate investigation.",
    },
    {
      kind: "truefalse",
      id: "ch4-q9",
      difficulty: "core",
      prompt:
        "A 64-bit signed integer storing cents can represent balances up to approximately $92 quadrillion, sufficient for any realistic banking amount.",
      answer: true,
      explanation:
        "A 64-bit signed integer holds up to ~9.2 × 10¹⁸. As a count of cents, that equals roughly $92 quadrillion — far beyond any real account or aggregate balance. Integer storage combines exactness with ample range.",
    },
    {
      kind: "mc",
      id: "ch4-q10",
      difficulty: "core",
      concept: "normal-balance",
      prompt:
        "Customer deposit accounts are liabilities. On which side of the account do liability balances grow?",
      options: [
        "Debit side — liabilities increase with debits",
        "Credit side — liabilities increase with credits",
        "Either side, depending on whether the transaction is incoming or outgoing",
        "Liability accounts have no normal balance — they always net to zero",
      ],
      answer: 1,
      explanation:
        "Liability accounts have a [[normal-balance|credit normal balance]]: credits increase the balance, debits decrease it. When a customer deposits cash, the bank credits their deposit account (liability goes up).",
    },
    {
      kind: "numeric",
      id: "ch4-q11",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A Customer Deposits subledger contains three accounts: Alice at $1,200, Bob at $800, and Carol at $500. What must the GL control account balance for Customer Deposits equal? (Enter dollars.)",
      answer: 2500,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The GL [[ledger-vs-subledger|control account]] must equal the sum of all subledger account balances: $1,200 + $800 + $500 = **$2,500**. This reconciliation holds because [[double-entry]] applies at every level — every posting hits both the subledger account and the GL control account simultaneously.",
    },
    {
      kind: "mc",
      id: "ch4-q12",
      difficulty: "core",
      concept: "account-type-asset",
      prompt:
        "The Loans subledger tracks amounts borrowers owe to the bank. What account type are individual loan balances?",
      options: [
        "Liability — loans are debts and therefore liabilities on both sides",
        "Equity — loans are funded by the bank's own capital",
        "Asset — the bank holds the right to repayment, which is an economic resource",
        "Revenue — loan balances generate interest income",
      ],
      answer: 2,
      explanation:
        "Outstanding loan balances are [[account-type-asset|assets]]: the bank owns the right to future repayment. Interest earned on loans is revenue, but the principal balance is an asset sitting in the Loans subledger.",
    },
    {
      kind: "mc",
      id: "ch4-q13",
      difficulty: "core",
      concept: "reserve-account",
      prompt:
        "A bank's reserve account — the funds it holds at the central bank — sits in which part of the GL hierarchy?",
      options: [
        "The Customer Deposits subledger, because reserves back customer balances",
        "A subledger dedicated to the bank's own assets (e.g., 'Bank Assets' or 'Interbank Balances')",
        "The Revenue subledger, because reserves earn interest at the central bank",
        "Directly in the GL with no subledger — reserves are exempt from subledger grouping",
      ],
      answer: 1,
      explanation:
        "The reserve account is a [[reserve-account|bank asset]]: funds the bank holds at the central bank. Like any asset, it lives in a subledger (such as 'Bank Assets') that rolls up into a GL asset control account.",
    },
    {
      kind: "truefalse",
      id: "ch4-q14",
      difficulty: "core",
      concept: "central-bank-reserves",
      prompt:
        "From a commercial bank's perspective, the funds it holds as central bank reserves are a liability — the central bank owes those funds back to the commercial bank.",
      answer: false,
      explanation:
        "[[central-bank-reserves]] are an **asset** from the commercial bank's perspective: they represent money the commercial bank owns and holds at the central bank. (They are a liability on the central bank's balance sheet, not on the commercial bank's.)",
    },
    {
      kind: "multi",
      id: "ch4-q15",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt:
        "Which of the following are true of a bank's Revenue subledger and its accounts? (Select all that apply.)",
      options: [
        "Fee Income accounts in the Revenue subledger are revenue-type accounts",
        "Revenue account balances roll up into a GL control account, just like any other subledger",
        "Revenue accounts have a debit normal balance — earned fees increase with debits",
        "At year-end, revenue account balances flow into retained earnings (equity)",
        "Revenue subledger balances are permanent and never transfer to another account",
      ],
      answers: [0, 1, 3],
      explanation:
        "Fee Income is an [[account-type-revenue|revenue]] account (option a). Like all subledgers, the Revenue subledger reconciles to a GL control account (option b). Revenue accounts have a **credit** normal balance, not debit (option c is wrong). At year-end, revenue balances close into retained earnings (option d); they are temporary, not permanent (option e is wrong).",
    },
    {
      kind: "mc",
      id: "ch4-q16",
      difficulty: "challenge",
      prompt:
        "A developer stores a payment as the result of `0.1 + 0.2` in binary floating-point, then compares it to `0.30`. The comparison returns `false`. What is the root cause?",
      options: [
        "Binary floating-point cannot represent `0.1` or `0.2` exactly, so their sum is slightly above `0.30`",
        "The database silently rounded the stored value during the write",
        "JavaScript integer overflow occurs at values near `0.30`",
        "The comparison should use `==` instead of `===` to allow type coercion",
      ],
      answer: 0,
      explanation:
        "IEEE 754 binary floating-point has no exact representation for `0.1` or `0.2`. Their sum evaluates to approximately `0.30000000000000004`. This is exactly why [[amount-cents|integer cents]] are used — integer arithmetic is always exact and this class of bug is impossible.",
    },
    {
      kind: "multi",
      id: "ch4-q17",
      difficulty: "challenge",
      prompt:
        "Which statements correctly describe the relationship between the General Ledger and subledgers? (Select all that apply.)",
      options: [
        "The GL control account balance must equal the sum of its subledger account balances at all times",
        "A subledger may mix account types (e.g., Assets and Liabilities) in the same subledger",
        "The GL provides a summary view; subledgers provide per-entity detail",
        "Subledgers are optional convenience views — banks may post everything directly to the GL if they choose",
        "Individual customer deposit accounts live in a subledger, not directly in the GL",
      ],
      answers: [0, 2, 4],
      explanation:
        "The [[ledger-vs-subledger]] hierarchy requires: (a) reconciliation between subledger totals and GL control accounts is mandatory; (c) the GL summarizes what subledgers detail; (e) individual accounts always live in subledgers. The book's examples show each subledger grouping **related** accounts — Customer Deposits groups Liability accounts, Loans groups Asset accounts — but the hierarchy is purely organizational, making this the practical standard for managing millions of accounts rather than a rigid type rule.",
    },
    {
      kind: "multi",
      id: "ch4-q18",
      difficulty: "challenge",
      prompt:
        "A bank discovers its Customer Deposits subledger sums to $10,050,000 while the GL control account shows $10,000,000. Which conditions could explain this $50,000 discrepancy? (Select all that apply.)",
      options: [
        "A posting credited a subledger account but the corresponding GL control account entry was omitted",
        "A new subledger account was opened and credited without a matching GL control account posting",
        "This is a normal rounding phenomenon — the GL works to the nearest $50,000 in large banks",
        "A GL reversal was posted without reversing the matching subledger entry",
      ],
      answers: [0, 1, 3],
      explanation:
        "Any mismatch between a [[ledger-vs-subledger|subledger total and its GL control account]] is a ledger error. Valid causes include: a posting that updated only one level (options a and b), or a reversal applied asymmetrically (option d). The GL never rounds — it must agree with its subledger to the cent.",
    },
    {
      kind: "numeric",
      id: "ch4-q19",
      difficulty: "challenge",
      concept: "account-type-asset",
      prompt:
        "A bank's Loans subledger opens the month with $2,480,000 in outstanding balances. During the month, new loans totaling $375,000 are disbursed and principal repayments of $130,000 are received. What is the closing Loans subledger total in dollars?",
      answer: 2725000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Loan balances are [[account-type-asset|assets]] — disbursements increase the asset (the bank gains a new receivable) and principal repayments decrease it (the receivable is partially extinguished). $2,480,000 + $375,000 − $130,000 = **$2,725,000**. The GL Asset control account for Loans must show this same total.",
    },
    {
      kind: "mc",
      id: "ch4-q20",
      difficulty: "challenge",
      prompt:
        "Some interest accruals require precision finer than one cent (e.g., 1/1,000th of a cent). According to the book, what is the correct approach?",
      options: [
        "Use floating-point arithmetic for accruals and round to integer cents for the final posting",
        "Choose an even smaller integer unit (e.g., milli-cents) so the accounting engine still works entirely in integers",
        "Use a DECIMAL type with 10 decimal places — exact, but still a decimal representation",
        "Round every accrual to the nearest cent before posting to maintain simplicity",
      ],
      answer: 1,
      explanation:
        "The book extends the same principle: when sub-cent precision is needed, define a smaller integer unit (milli-cents, millionths, etc.) rather than reaching for floating-point. The accounting engine always works in integers — only the denomination of the unit changes.",
    },
  ],
};
