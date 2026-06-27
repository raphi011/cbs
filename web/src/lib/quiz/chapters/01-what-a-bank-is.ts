import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "01-what-a-bank-is",
  number: 1,
  part: "Part I · The Foundations of Bank Accounting",
  title: "What a Bank Is",
  questions: [
    {
      kind: "mc",
      id: "ch1-q1",
      difficulty: "intro",
      concept: "account-type-liability",
      prompt:
        "A customer deposits $500 cash at a bank. From the bank's perspective, the customer's deposit balance is:",
      options: [
        "An asset — the bank now holds the customer's cash",
        "A liability — the bank owes the customer $500 on demand",
        "Revenue — the bank earned income from receiving the deposit",
        "Equity — the customer becomes a part-owner of the bank",
      ],
      answer: 1,
      explanation:
        "When you deposit money, the bank records a [[account-type-liability]]: it owes you that amount back on demand. The physical cash becomes the bank's property (an [[account-type-asset]]); your account balance is the bank's debt to you — a promise to pay.",
    },
    {
      kind: "truefalse",
      id: "ch1-q2",
      difficulty: "intro",
      concept: "account-type-asset",
      prompt:
        "Cash held in the bank's vault is recorded as an asset on the bank's balance sheet.",
      answer: true,
      explanation:
        "Cash is something the bank owns — it is an [[account-type-asset]]. When a customer deposits physical notes, those notes become the bank's property. The customer receives a deposit claim in return, which is an obligation the bank records as a [[account-type-liability]].",
    },
    {
      kind: "mc",
      id: "ch1-q3",
      difficulty: "intro",
      prompt: "What is the primary function of a core banking system?",
      options: [
        "Managing physical vault security and ATM cash logistics",
        "Serving as the authoritative record of all of an institution's financial activity",
        "Routing card transactions to merchant terminals",
        "Calculating regulatory capital requirements for the regulator",
      ],
      answer: 1,
      explanation:
        "A core banking system is the *system of record* for every deposit, withdrawal, transfer, loan, and fee. Every other channel — mobile app, ATM network, card rails, regulatory reports — ultimately reads from or writes to this single source of truth.",
      explore: { label: "See the ledger", href: "/" },
    },
    {
      kind: "truefalse",
      id: "ch1-q4",
      difficulty: "intro",
      concept: "double-entry",
      prompt:
        "In double-entry bookkeeping, the total of all debits in a transaction must equal the total of all credits.",
      answer: true,
      explanation:
        "This is the core rule of [[double-entry]]: money never appears or disappears — every transaction moves value from one account to another, producing equal and opposite entries. The balance between debits and credits is a built-in error-detection mechanism.",
    },
    {
      kind: "numeric",
      id: "ch1-q5",
      difficulty: "intro",
      concept: "account-type-equity",
      prompt:
        "A bank reports $400,000 in total assets and $360,000 in total liabilities. Using the accounting equation (Assets = Liabilities + Equity), what is the bank's equity? (Enter a number of dollars.)",
      answer: 40000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[account-type-equity]] = Assets − Liabilities = $400,000 − $360,000 = **$40,000**. Equity is the owners' residual stake — what would be left for shareholders if every asset were sold at book value and every liability paid off.",
    },
    {
      kind: "mc",
      id: "ch1-q6",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A customer deposits $500 cash. Which pair of ledger entries correctly records this transaction?",
      options: [
        "Debit Cash +$500 · Credit Customer Deposit Liability +$500",
        "Debit Customer Deposit Liability +$500 · Credit Cash +$500",
        "Debit Cash +$500 · Credit Revenue +$500",
        "Debit Equity +$500 · Credit Cash +$500",
      ],
      answer: 0,
      explanation:
        "[[double-entry]] requires equal and opposite entries. Cash (an asset) has a debit normal balance, so it increases on the debit side. The customer's deposit account (a liability) has a credit normal balance, so it increases on the credit side. Both sides of Assets = Liabilities + Equity rise by $500.",
    },
    {
      kind: "truefalse",
      id: "ch1-q7",
      difficulty: "core",
      concept: "account-type-equity",
      prompt:
        "When a customer withdraws $200 from their account, the bank's equity decreases by $200.",
      answer: false,
      explanation:
        "A withdrawal reduces cash (an asset) by $200 and simultaneously reduces the deposit liability by $200. Both sides of Assets = Liabilities + [[account-type-equity]] shrink equally, leaving equity unchanged. Equity changes only when revenue or expenses arise, or when owners inject or withdraw capital.",
    },
    {
      kind: "mc",
      id: "ch1-q8",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A bank receives a new $1,000,000 deposit. What is the net effect on the accounting equation Assets = Liabilities + Equity?",
      options: [
        "Assets +$1,000,000 · Liabilities +$1,000,000 · Equity unchanged",
        "Assets +$1,000,000 · Equity +$1,000,000 · Liabilities unchanged",
        "Liabilities +$1,000,000 · Equity −$1,000,000 · Assets unchanged",
        "The equation is temporarily out of balance until the cash is invested",
      ],
      answer: 0,
      explanation:
        "[[double-entry]] guarantees the equation never breaks. A deposit adds cash (asset +$1M) and the obligation to repay it (liability +$1M). Both sides grow equally, so equity is untouched. A bank growing its deposit base is richer in assets and deeper in debt at the same time — that is completely normal.",
    },
    {
      kind: "multi",
      id: "ch1-q9",
      difficulty: "core",
      concept: "account-type-asset",
      prompt:
        "Which of the following are assets on a bank's balance sheet? (Select all that apply.)",
      options: [
        "Cash held in the vault",
        "Customer savings account balances",
        "Loans the bank has extended to borrowers",
        "Bonds the bank has issued to investors",
        "Reserves held at the central bank",
      ],
      answers: [0, 2, 4],
      explanation:
        "[[account-type-asset]]s are things the bank owns or is owed: vault cash, loans it has made (borrowers owe the bank), and reserves at the central bank. Customer savings balances and issued bonds are **liabilities** — obligations the bank owes to others.",
    },
    {
      kind: "multi",
      id: "ch1-q10",
      difficulty: "core",
      concept: "account-type-liability",
      prompt:
        "Which of the following are liabilities on a bank's balance sheet? (Select all that apply.)",
      options: [
        "Loans the bank has extended to retail customers",
        "Customer checking account balances",
        "Money borrowed overnight from other banks",
        "Bonds the bank has issued to raise capital",
        "Securities held in the bank's investment portfolio",
      ],
      answers: [1, 2, 3],
      explanation:
        "[[account-type-liability]]s are what the bank owes: customer deposits, interbank borrowings, and bonds it has issued. Loans made to customers and held securities are [[account-type-asset]]s — the bank owns or is owed those.",
    },
    {
      kind: "mc",
      id: "ch1-q11",
      difficulty: "core",
      concept: "normal-balance",
      prompt: "What is the normal balance direction of a liability account?",
      options: [
        "Debit — liabilities increase on the debit side",
        "Credit — liabilities increase on the credit side",
        "Either debit or credit, depending on the specific liability",
        "Liabilities have no normal balance; they net to zero over time",
      ],
      answer: 1,
      explanation:
        "[[normal-balance]] is the side that increases an account. Liabilities, equity, and revenue all have a **credit** normal balance — they grow when credited and shrink when debited. Assets and expenses are the opposite: debit normal. This is why crediting a deposit account records that the bank owes the customer more.",
    },
    {
      kind: "mc",
      id: "ch1-q12",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt:
        "Interest that a bank earns on outstanding loans it has issued is classified as:",
      options: [
        "An asset — it increases the outstanding loan balance",
        "A liability — it represents an obligation to depositors",
        "Revenue — it is income earned by the bank",
        "Equity — it directly increases the shareholders' stake",
      ],
      answer: 2,
      explanation:
        "[[account-type-revenue]] accounts capture income the bank earns. Interest on loans is a bank's primary revenue source. Revenue is a temporary account that eventually flows into retained earnings, increasing [[account-type-equity]] at period end — but during the period it sits in revenue, not equity.",
    },
    {
      kind: "mc",
      id: "ch1-q13",
      difficulty: "core",
      concept: "account-type-expense",
      prompt:
        "A bank pays $5,000 in interest to its depositors this month. How is this recorded in the bank's accounts?",
      options: [
        "As an asset — the bank will recover this outflow in future periods",
        "As a reduction in the deposit liability — the deposit shrinks",
        "As an expense — it is a cost the bank incurs to fund itself",
        "As a direct reduction in equity — it bypasses the income statement",
      ],
      answer: 2,
      explanation:
        "Interest paid to depositors is an [[account-type-expense]]: a cost the bank incurs to attract and hold customer funds. Expenses reduce net income and ultimately reduce [[account-type-equity]] at period end. The bank's goal is to earn more in loan interest ([[account-type-revenue]]) than it pays out in deposit interest.",
    },
    {
      kind: "numeric",
      id: "ch1-q14",
      difficulty: "core",
      concept: "account-type-liability",
      prompt:
        "A customer deposits $750 cash at the bank. By how many dollars do the bank's total liabilities increase? (Enter a number of dollars.)",
      answer: 750,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The deposit creates a new [[account-type-liability]] of $750 — the bank now owes the customer that amount on demand. Simultaneously, cash (an asset) rises by $750. Both sides of the accounting equation increase by exactly **$750**, and total liabilities increase by $750.",
    },
    {
      kind: "mc",
      id: "ch1-q15",
      difficulty: "core",
      concept: "balance-book",
      prompt:
        "Why do real banks maintain a full transaction ledger rather than simply storing one balance number per customer?",
      options: [
        "Regulators require it, but it offers no practical benefit over a single-balance system",
        "A ledger lets the bank detect errors, trace the reason for every balance change, and distinguish recorded credits from funds that have physically arrived",
        "Storing one balance per customer would require more disk space than a full ledger",
        "A single balance number cannot represent values above $999,999",
      ],
      answer: 1,
      explanation:
        "A [[balance-book]] derived from immutable ledger entries provides three things a balance-only store cannot: error detection (debits must equal credits), a full audit trail (every change has a documented cause), and the ability to separate *booked* from *settled* funds — a distinction that governs interest, availability, and risk.",
      explore: { label: "See the ledger", href: "/" },
    },
    {
      kind: "mc",
      id: "ch1-q16",
      difficulty: "challenge",
      concept: "account-type-asset",
      prompt:
        "A bank creates a $10,000 loan by crediting the borrower's deposit account (without disbursing physical cash). Immediately after booking, which statement about the bank's balance sheet is correct?",
      options: [
        "Total assets increase by $10,000 and total liabilities increase by $10,000; equity is unchanged",
        "Total assets increase by $10,000 and equity increases by $10,000; liabilities are unchanged",
        "Total assets and liabilities are both unchanged because the loan and deposit offset each other",
        "Equity decreases by $10,000 to reflect the credit risk the bank has taken on",
      ],
      answer: 0,
      explanation:
        "The bank gains a loan receivable (a new [[account-type-asset]] +$10,000) and books a matching deposit the borrower can spend (a new [[account-type-liability]] +$10,000). Both sides of Assets = Liabilities + Equity expand equally, so equity is unchanged.",
    },
    {
      kind: "multi",
      id: "ch1-q17",
      difficulty: "challenge",
      concept: "normal-balance",
      prompt:
        "Which of the following account types carry a credit normal balance — meaning they increase when credited and decrease when debited? (Select all that apply.)",
      options: [
        "Asset accounts",
        "Liability accounts",
        "Equity accounts",
        "Revenue accounts",
        "Expense accounts",
      ],
      answers: [1, 2, 3],
      explanation:
        "[[normal-balance]] divides the five account types into two groups. Liabilities, equity, and revenue all have a **credit** normal balance — they grow on the right side of the ledger. Assets and expenses have a **debit** normal balance — they grow on the left. Mixing these up produces entries that are backwards.",
    },
    {
      kind: "truefalse",
      id: "ch1-q18",
      difficulty: "challenge",
      concept: "account-type-revenue",
      prompt:
        "When a bank earns $50,000 in loan interest during the year with no other income or expenses, the bank's equity ultimately increases by $50,000 at year-end.",
      answer: true,
      explanation:
        "[[account-type-revenue]] accounts are temporary — they accumulate during the year and are closed at period end. Net income (revenue minus expenses) flows into retained earnings, which is part of [[account-type-equity]]. With $50,000 of interest revenue and no expenses, net income is $50,000 and equity rises by exactly that amount.",
    },
    {
      kind: "numeric",
      id: "ch1-q19",
      difficulty: "challenge",
      concept: "account-type-equity",
      prompt:
        "A bank starts with $5,000,000 in total assets and $4,400,000 in total liabilities. It then takes a $500,000 cash deposit and immediately lends $500,000 to a borrower by crediting the borrower's deposit account. What is the bank's equity after both transactions? (Enter a number of dollars.)",
      answer: 600000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Initial [[account-type-equity]] = $5,000,000 − $4,400,000 = $600,000. The cash deposit adds $500k to assets and $500k to liabilities equally — equity unchanged. The loan adds a $500k receivable (asset) and a $500k deposit for the borrower (liability) equally — equity still unchanged. Every transaction keeps both sides in lockstep, so equity remains **$600,000**.",
    },
    {
      kind: "mc",
      id: "ch1-q20",
      difficulty: "challenge",
      concept: "account-type-revenue",
      prompt:
        "A bank collects a $25 monthly maintenance fee in cash from a customer. What is the immediate effect on the bank's balance sheet?",
      options: [
        "Assets +$25, liabilities +$25, equity unchanged",
        "Assets +$25, equity +$25 (via revenue), liabilities unchanged",
        "Liabilities −$25, equity +$25, assets unchanged",
        "No balance-sheet effect — fees are off-balance-sheet items",
      ],
      answer: 1,
      explanation:
        "The bank receives $25 cash (an asset rises by $25) and records [[account-type-revenue]] of $25. Because no new liability is created, the equation stays balanced by equity rising $25 — revenue flows into net income and then retained earnings. Compare this with a deposit: both bring in cash, but a deposit also creates a liability, whereas a fee creates revenue (and ultimately equity).",
    },
  ],
};
