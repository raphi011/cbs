import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "03-the-chart-of-accounts",
  number: 3,
  part: "Part I · The Foundations of Bank Accounting",
  title: "The Chart of Accounts",
  questions: [
    {
      kind: "mc",
      id: "ch3-q1",
      difficulty: "intro",
      concept: "normal-balance",
      prompt: "An account's 'normal balance' is the side (debit or credit) that increases it. What is the normal balance of a bank's Cash account?",
      options: [
        "Credit — cash increases when you credit it",
        "Debit — cash is an asset and assets have a debit normal balance",
        "Either side — cash is neutral",
        "It depends on the bank's chart of accounts",
      ],
      answer: 1,
      explanation:
        "Assets carry a **debit [[normal-balance]]**. To increase an asset like Cash you debit it; to decrease it you credit it.",
    },
    {
      kind: "multi",
      id: "ch3-q2",
      difficulty: "intro",
      concept: "normal-balance",
      prompt: "Which account types have a DEBIT normal balance? (Select all that apply.)",
      options: [
        "Assets",
        "Liabilities",
        "Equity",
        "Revenue",
        "Expenses",
      ],
      answers: [0, 4],
      explanation:
        "[[normal-balance]] by type: Assets and Expenses are **debit-normal** (increases are debits). Liabilities, Equity, and Revenue are **credit-normal** (increases are credits).",
    },
    {
      kind: "truefalse",
      id: "ch3-q3",
      difficulty: "intro",
      concept: "account-type-revenue",
      prompt: "Revenue accounts are temporary: they are closed into retained earnings (equity) at year-end.",
      answer: true,
      explanation:
        "[[account-type-revenue]] and [[account-type-expense]] accounts are **temporary** (income statement accounts). At year-end their net balance is transferred to retained earnings, an equity account, resetting them to zero for the new period.",
    },
    {
      kind: "mc",
      id: "ch3-q4",
      difficulty: "core",
      concept: "account-type-expense",
      prompt: "Interest paid to depositors appears in the bank's books as:",
      options: [
        "A debit to an Expense account",
        "A credit to a Revenue account",
        "A debit to a Liability account",
        "A credit to an Asset account",
      ],
      answer: 0,
      explanation:
        "Interest paid to depositors is a cost of funding — it is an [[account-type-expense]]. Expenses have a debit normal balance, so recording the expense is a debit to the interest expense account.",
    },
    {
      kind: "mc",
      id: "ch3-q5",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt: "A bank earns $5,000 interest from a borrower repaying a loan. This is recorded as:",
      options: [
        "Debit Interest Revenue +$5,000",
        "Credit Interest Revenue +$5,000",
        "Debit Cash +$5,000 only",
        "Credit Loan Receivable +$5,000 only",
      ],
      answer: 1,
      explanation:
        "[[account-type-revenue]] has a credit normal balance, so earning interest is a **credit** to Interest Revenue (and a debit to Cash or Loan Receivable). This is the bank's net interest margin.",
    },
    {
      kind: "multi",
      id: "ch3-q6",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt: "Which of the following are sources of bank revenue? (Select all that apply.)",
      options: [
        "Interest earned on loans issued",
        "Salaries paid to employees",
        "Transaction and service fees charged to customers",
        "Interest paid to depositors",
        "Trading gains on securities",
      ],
      answers: [0, 2, 4],
      explanation:
        "A bank's [[account-type-revenue]] comes from interest income (net interest margin), fees, and trading gains. Salaries and interest paid to depositors are **expenses**.",
    },
    {
      kind: "truefalse",
      id: "ch3-q7",
      difficulty: "core",
      concept: "normal-balance",
      prompt:
        "To record a new customer deposit of $1,000, you would credit the Customer Deposits liability account because credits increase liabilities.",
      answer: true,
      explanation:
        "Liabilities have a **credit [[normal-balance]]**. A new deposit increases the bank's obligation to the customer, so it is recorded as a credit to the Customer Deposits liability account.",
    },
    {
      kind: "mc",
      id: "ch3-q8",
      difficulty: "challenge",
      concept: "account-type-equity",
      prompt: "The expanded accounting equation is: Assets = Liabilities + Equity + Revenue − Expenses. At year-end, revenue of $800,000 and expenses of $600,000 are closed. What is the net change to Equity?",
      options: [
        "+$200,000",
        "+$800,000",
        "−$600,000",
        "$0 — closing entries cancel out",
      ],
      answer: 0,
      explanation:
        "Closing transfers net income (Revenue − Expenses = $800,000 − $600,000 = **$200,000**) into retained earnings, an [[account-type-equity]] account. Equity increases by $200,000.",
    },
    {
      kind: "mc",
      id: "ch3-q9",
      difficulty: "challenge",
      concept: "normal-balance",
      prompt:
        "A bank charges a loan-loss provision of $50,000, recognising that some loans may not be repaid. The provision is debited to Loan Loss Provision Expense. What is the offsetting credit?",
      options: [
        "Credit Allowance for Loan Losses (a contra-asset account)",
        "Credit Cash — the bank sets aside actual cash",
        "Credit Revenue — provisioning generates income",
        "Credit Retained Earnings directly",
      ],
      answer: 0,
      explanation:
        "A loan-loss provision debits an expense (increasing it, consistent with its debit [[normal-balance]]) and credits an **allowance for loan losses**, a contra-asset that reduces the net carrying value of the loan portfolio. No cash moves yet.",
    },
    {
      kind: "multi",
      id: "ch3-q10",
      difficulty: "challenge",
      concept: "account-type-expense",
      prompt:
        "Which of the following are bank expenses? (Select all that apply.)",
      options: [
        "Interest paid on customer savings deposits",
        "Net interest margin earned on loans",
        "Employee salaries and benefits",
        "Loan-loss provisions",
        "Fee income from payment services",
      ],
      answers: [0, 2, 3],
      explanation:
        "[[account-type-expense]]s reduce profit: interest paid to depositors, salaries, and loan-loss provisions all appear on the expense side. Net interest margin and fee income are **revenue** items.",
    },
  ],
};
