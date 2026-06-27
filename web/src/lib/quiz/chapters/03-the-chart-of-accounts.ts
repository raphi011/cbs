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
      prompt:
        "An account's 'normal balance' is the side (debit or credit) that increases it. What is the normal balance of a bank's Cash account?",
      options: [
        "Credit — cash increases when you credit it",
        "Debit — cash is an asset and assets have a debit normal balance",
        "Either side — cash can be increased by either entry",
        "It depends on the bank's chart of accounts",
      ],
      answer: 1,
      explanation:
        "Assets carry a **debit [[normal-balance]]**. To increase an asset like Cash you debit it; to decrease it you credit it. This holds for every asset account, from vault cash to loan receivables.",
    },
    {
      kind: "multi",
      id: "ch3-q2",
      difficulty: "intro",
      concept: "normal-balance",
      prompt:
        "Which account types have a DEBIT normal balance? (Select all that apply.)",
      options: [
        "Assets",
        "Liabilities",
        "Equity",
        "Revenue",
        "Expenses",
      ],
      answers: [0, 4],
      explanation:
        "[[normal-balance]] by type: Assets and Expenses are **debit-normal** — a debit increases them. Liabilities, Equity, and Revenue are **credit-normal** — a credit increases them. The neat symmetry: the two 'cost' sides of the equation (what you own, what you spend) increase with debits; the three 'source' sides (what you owe, the owners' stake, what you earn) increase with credits.",
    },
    {
      kind: "truefalse",
      id: "ch3-q3",
      difficulty: "intro",
      concept: "account-type-revenue",
      prompt:
        "Revenue accounts are temporary: they are closed into retained earnings (equity) at year-end and reset to zero.",
      answer: true,
      explanation:
        "[[account-type-revenue]] and [[account-type-expense]] accounts are **temporary** income statement accounts. At year-end their net balance — net income — is transferred to retained earnings, an equity account, resetting both to zero for the new period. This closing step is the hinge connecting the income statement to the balance sheet.",
    },
    {
      kind: "mc",
      id: "ch3-q4",
      difficulty: "intro",
      concept: "account-type-asset",
      prompt:
        "A bank extends a $200,000 mortgage to a customer and disburses the funds from its cash reserves. What happens to the bank's total assets?",
      options: [
        "Total assets decrease by $200,000 — the bank spent its cash",
        "Total assets stay the same — cash converts into a loan receivable, both assets",
        "Total assets increase by $200,000 — the bank gained a new claim on the borrower",
        "Total assets decrease and liabilities increase — a new obligation was created",
      ],
      answer: 1,
      explanation:
        "Issuing a mortgage is an [[account-type-asset]]-to-asset conversion: the bank debits 'Loans Receivable' (+$200,000) and credits 'Cash' (−$200,000). Both accounts are assets, so total assets are unchanged — the bank simply holds a different mix. The customer's promise to repay is itself a valuable asset.",
    },
    {
      kind: "truefalse",
      id: "ch3-q5",
      difficulty: "intro",
      concept: "account-type-expense",
      prompt:
        "Salaries paid to employees and loan-loss provisions set aside against expected defaults are both classified as expense accounts.",
      answer: true,
      explanation:
        "Both are [[account-type-expense]] items: salaries are a direct cost of running the bank; loan-loss provisions are the amount set aside in anticipation of borrower defaults. Both reduce the bank's profit and appear on the income statement, not the balance sheet.",
    },
    {
      kind: "mc",
      id: "ch3-q6",
      difficulty: "core",
      concept: "account-type-expense",
      prompt:
        "Interest paid to depositors appears in the bank's books as:",
      options: [
        "A debit to an Expense account",
        "A credit to a Revenue account",
        "A debit to a Liability account",
        "A credit to an Asset account",
      ],
      answer: 0,
      explanation:
        "Interest paid to depositors is a cost of funding — it is an [[account-type-expense]]. Expenses have a debit normal balance, so recording the cost is a debit to Interest Expense. The offsetting credit goes to the customer's deposit liability, which increases — the bank now owes the saver a little more.",
    },
    {
      kind: "mc",
      id: "ch3-q7",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt:
        "A bank earns $5,000 in interest from a borrower's monthly loan repayment. How is the interest income recorded?",
      options: [
        "Debit Interest Income $5,000",
        "Credit Interest Income $5,000",
        "Credit Cash $5,000 only",
        "Debit Loan Receivable $5,000 only",
      ],
      answer: 1,
      explanation:
        "[[account-type-revenue]] has a **credit normal balance**, so earned interest is a **credit** to Interest Income (increasing it). The matching debit goes to Cash or reduces the Loan Receivable. The net interest margin — lending at a higher rate than the bank pays depositors — is how banks make most of their money.",
    },
    {
      kind: "multi",
      id: "ch3-q8",
      difficulty: "core",
      concept: "account-type-revenue",
      prompt:
        "Which of the following are sources of revenue for a bank? (Select all that apply.)",
      options: [
        "Interest earned on loans and mortgages",
        "Salaries and benefits paid to staff",
        "Transaction and service fees charged to customers",
        "Interest paid on customer savings accounts",
        "Trading gains from buying and selling securities",
      ],
      answers: [0, 2, 4],
      explanation:
        "A bank's [[account-type-revenue]] comes from three main streams: **interest income** (lending at rates above deposit costs), **fee income** (account fees, wire fees, ATM fees), and **trading gains**. Salaries and interest paid to depositors are expenses — they consume revenue, not create it.",
    },
    {
      kind: "truefalse",
      id: "ch3-q9",
      difficulty: "core",
      concept: "normal-balance",
      prompt:
        "When a bank records cash received from any source, it debits its Cash account. This debit increases Cash because Cash is an asset with a debit normal balance.",
      answer: true,
      explanation:
        "Assets have a **debit [[normal-balance]]**: a debit increases the balance, a credit decreases it. Receiving cash means the bank holds more of a valuable asset, so it records a debit to the Cash account. This is the mirror of the credit normal balance for liability accounts — the two sides of the normal-balance table always move in opposite directions.",
    },
    {
      kind: "mc",
      id: "ch3-q10",
      difficulty: "core",
      concept: "ledger-vs-subledger",
      prompt:
        "A bank's General Ledger shows one line: 'Customer Deposits — $10,000,000.' The Customer Deposits subledger contains 50,000 individual accounts that sum to that total. What does the subledger provide that the GL line alone cannot?",
      options: [
        "The subledger corrects arithmetic errors in the GL total",
        "The subledger shows the balance for each individual customer account",
        "The subledger records entries that are excluded from the GL",
        "The subledger stores only asset accounts while the GL stores liabilities",
      ],
      answer: 1,
      explanation:
        "In a [[ledger-vs-subledger]] hierarchy, the General Ledger holds *summary* totals while the subledger holds the *detail* — every individual account. A bank may have millions of deposit accounts; the GL rolls them up into a single line, but the subledger tracks what each customer is owed so that per-customer statements and balance checks are possible.",
    },
    {
      kind: "mc",
      id: "ch3-q11",
      difficulty: "core",
      concept: "account-type-liability",
      prompt:
        "A teller entry reads: 'Credit Alice's Deposit Account $500.' What does this entry mean for Alice's balance?",
      options: [
        "Alice's balance decreases by $500 — credits decrease account balances",
        "Alice's balance increases by $500 — credits increase liabilities, and her deposit is the bank's liability",
        "The entry is an error; deposit accounts can only be debited",
        "Alice's balance is unchanged — credits only affect asset accounts",
      ],
      answer: 1,
      explanation:
        "A customer's deposit account is an [[account-type-liability]] — the bank owes that money to the customer. Liabilities have a credit normal balance, so a **credit increases** the liability, meaning the bank owes Alice more. From Alice's perspective this appears as a higher balance; from the bank's perspective its obligation grew.",
    },
    {
      kind: "mc",
      id: "ch3-q12",
      difficulty: "core",
      prompt:
        "A bank pays $10 monthly interest to a saver. Which pair of entries correctly records this transaction?",
      options: [
        "Debit Interest Expense $10 / Credit Customer Deposit $10",
        "Credit Interest Expense $10 / Debit Customer Deposit $10",
        "Debit Cash $10 / Credit Interest Income $10",
        "Debit Customer Deposit $10 / Credit Cash $10",
      ],
      answer: 0,
      explanation:
        "The bank incurred a cost ([[account-type-expense]] — debit-normal, so debit to record it) and owes the saver more ([[account-type-liability]] — credit-normal, so credit to increase it). Debit Interest Expense to record the cost; credit the Customer Deposit to show the bank owes $10 more. No cash changes hands yet — the saver's balance simply grows.",
    },
    {
      kind: "mc",
      id: "ch3-q13",
      difficulty: "core",
      prompt:
        "The balance sheet and income statement report different sets of account types. Which two account types appear on the income statement but are NOT carried forward on the year-end balance sheet?",
      options: [
        "Assets and Liabilities",
        "Equity and Assets",
        "Revenue and Expenses",
        "Liabilities and Equity",
      ],
      answer: 2,
      explanation:
        "Revenue and Expense accounts are **temporary** — they measure activity over a period and feed the income statement. At year-end they are closed (zeroed out) and their net result rolls into retained earnings on the **balance sheet**. Assets, Liabilities, and Equity are **permanent** accounts that carry their balances forward year after year.",
    },
    {
      kind: "numeric",
      id: "ch3-q14",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A bank's balance sheet shows total assets of $150,000,000 and total liabilities of $138,000,000. Using the accounting equation Assets = Liabilities + Equity, what is the bank's equity in dollars?",
      answer: 12000000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The accounting equation — the foundation of [[double-entry]] bookkeeping — rearranges to Equity = Assets − Liabilities = $150,000,000 − $138,000,000 = **$12,000,000**. Equity is the residual: what belongs to the owners after all obligations are subtracted from everything the bank owns.",
    },
    {
      kind: "mc",
      id: "ch3-q15",
      difficulty: "challenge",
      concept: "account-type-equity",
      prompt:
        "The expanded accounting equation is: Assets = Liabilities + Equity + Revenue − Expenses. At year-end, a bank's revenue accounts total $800,000 and expense accounts total $600,000. When these are closed into retained earnings, what is the net change to Equity?",
      options: [
        "+$200,000",
        "+$800,000",
        "−$600,000",
        "$0 — closing entries cancel out",
      ],
      answer: 0,
      explanation:
        "Closing transfers net income (Revenue − Expenses = $800,000 − $600,000 = **$200,000**) into retained earnings, an [[account-type-equity]] account. Equity increases by $200,000 — the period's profit becomes a permanent part of the owners' stake. Revenue and expense accounts then reset to zero.",
    },
    {
      kind: "multi",
      id: "ch3-q16",
      difficulty: "challenge",
      concept: "account-type-expense",
      prompt:
        "Which of the following are classified as bank expenses? (Select all that apply.)",
      options: [
        "Interest paid on customer savings deposits",
        "Net interest margin earned on loans",
        "Employee salaries and benefits",
        "Loan-loss provisions for expected defaults",
        "Fee income from payment services",
      ],
      answers: [0, 2, 3],
      explanation:
        "[[account-type-expense]]s reduce profit: interest paid to depositors, salaries, and loan-loss provisions all appear on the expense side of the income statement. Net interest margin and fee income are **revenue** items — money flowing into the bank, not out of it.",
    },
    {
      kind: "truefalse",
      id: "ch3-q17",
      difficulty: "challenge",
      prompt:
        "A bank records $200 of fee income by debiting a Fee Revenue account, because debiting increases that account's balance.",
      answer: false,
      explanation:
        "Revenue accounts have a **credit normal balance** — a credit increases them, a debit *decreases* them. To record $200 of [[account-type-revenue|fee income]], the bank **credits** Fee Revenue (increasing it) and debits the matching asset (Cash or Receivable). Debiting a revenue account would reduce income, the opposite of what is intended.",
    },
    {
      kind: "mc",
      id: "ch3-q18",
      difficulty: "challenge",
      prompt:
        "A bank pays salaries of $5,000 in cash: 'Debit Salaries Expense $5,000 / Credit Cash $5,000.' What is the net long-term effect on the bank's equity?",
      options: [
        "Equity decreases by $5,000 — the expense reduces the owners' stake at year-end closing",
        "Equity increases by $5,000 — spending cash creates value for the bank",
        "Equity is unaffected — the entry only touches an expense and an asset account",
        "Equity decreases by $10,000 — both sides of the entry reduce equity",
      ],
      answer: 0,
      explanation:
        "Expense accounts are temporary: at year-end the $5,000 Salaries Expense is closed into retained earnings (an equity account), reducing equity by $5,000. Simultaneously, Cash (an asset) fell by $5,000 — so both assets and equity shrink by the same amount, keeping the accounting equation balanced. Assets = Liabilities + Equity on both sides.",
    },
    {
      kind: "numeric",
      id: "ch3-q19",
      difficulty: "challenge",
      prompt:
        "A bank holds a $200,000 mortgage loan at an annual interest rate of 4%. How many dollars of interest income does the bank earn from this mortgage in one year?",
      answer: 8000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Interest income = principal × rate = $200,000 × 4% = **$8,000**. This is an [[account-type-revenue]] item — the bank credits Interest Income $8,000 and debits Cash (or the loan balance) $8,000. Lending at a rate above what the bank pays depositors is the primary revenue engine for most banks.",
    },
    {
      kind: "numeric",
      id: "ch3-q20",
      difficulty: "challenge",
      prompt:
        "At the start of the year, a bank's retained earnings stand at $3,000,000. During the year it earns $700,000 in total revenue and incurs $450,000 in total expenses. After the year-end closing entries transfer net income into retained earnings, what are the retained earnings in dollars?",
      answer: 3250000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Net income = Revenue − Expenses = $700,000 − $450,000 = $250,000. This profit is closed into retained earnings: $3,000,000 + $250,000 = **$3,250,000**. Closing zeroes out the temporary [[account-type-revenue|revenue]] and [[account-type-expense|expense]] accounts and rolls their net result into the permanent equity section of the balance sheet.",
    },
  ],
};
