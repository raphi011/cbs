import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "02-double-entry-bookkeeping",
  number: 2,
  part: "Part I · The Foundations of Bank Accounting",
  title: "Double-Entry Bookkeeping",
  questions: [
    {
      kind: "mc",
      id: "ch2-q1",
      difficulty: "intro",
      concept: "double-entry",
      prompt: "Which statement best captures the double-entry rule?",
      options: [
        "Every transaction records equal total debits and credits",
        "Every transaction decreases one account and increases another",
        "Every debit entry must be matched by a credit the following business day",
        "Total assets must equal total equity after each transaction",
      ],
      answer: 0,
      explanation:
        "The single rule of [[double-entry]]: within any transaction, total debits = total credits. Value is conserved — it moves between accounts rather than appearing or disappearing.",
    },
    {
      kind: "mc",
      id: "ch2-q2",
      difficulty: "intro",
      concept: "normal-balance",
      prompt:
        "Your bank notifies you it has 'credited your account $80.' Your deposit balance has…",
      options: [
        "Increased by $80",
        "Decreased by $80",
        "Stayed the same — 'credited' is just confirmation jargon",
        "Changed in an unknown direction without more information",
      ],
      answer: 0,
      explanation:
        "Your deposit account is the bank's liability. Liabilities have a credit [[normal-balance]] — credits increase them. A credit to your account adds $80, even though everyday language sometimes pairs 'credit' with 'reduction.'",
    },
    {
      kind: "mc",
      id: "ch2-q3",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A customer deposits $100 in cash. Which pair of postings correctly records this transaction?",
      options: [
        "Debit Cash $100; Credit Customer Deposit $100",
        "Credit Cash $100; Debit Customer Deposit $100",
        "Debit Cash $100; Debit Customer Deposit $100",
        "Credit Cash $100; Credit Customer Deposit $100",
      ],
      answer: 0,
      explanation:
        "Cash is an asset with a debit [[normal-balance]] — it rises with a debit. The customer's deposit account is a liability with a credit normal balance — it rises with a credit. Debit $100 = Credit $100, so the entry balances. See [[double-entry]].",
    },
    {
      kind: "mc",
      id: "ch2-q4",
      difficulty: "core",
      concept: "normal-balance",
      prompt:
        "Which of the following account types increases with a DEBIT entry?",
      options: [
        "Asset",
        "Liability",
        "Revenue",
        "Equity",
      ],
      answer: 0,
      explanation:
        "Assets (and expenses) have a debit [[normal-balance]]: they grow with debits and shrink with credits. Liabilities, equity, and revenue are the opposite — they grow with credits and shrink with debits.",
    },
    {
      kind: "mc",
      id: "ch2-q5",
      difficulty: "core",
      prompt:
        "A trial balance shows total debit balances equal to total credit balances. What does this tell you?",
      options: [
        "The records are internally consistent",
        "Every posting went to the correct account",
        "No accounting errors have occurred",
        "The bank's assets equal its equity",
      ],
      answer: 0,
      explanation:
        "A balanced trial balance confirms the [[double-entry]] rule was obeyed: every posted transaction was balanced. It cannot detect errors where the correct *amount* was posted to the *wrong* (but still balancing) account.",
    },
    {
      kind: "mc",
      id: "ch2-q6",
      difficulty: "core",
      prompt:
        "Customer A transfers $60 to Customer B — both hold accounts at the same bank. Which postings record this transfer?",
      options: [
        "Debit Customer A's deposit; Credit Customer B's deposit",
        "Credit Customer A's deposit; Debit Customer B's deposit",
        "Debit the bank's Cash; Credit Customer B's deposit",
        "Credit Customer A's deposit; Credit Customer B's deposit",
      ],
      answer: 0,
      explanation:
        "Customer A's deposit (an [[account-type-liability]]) is debited — the bank owes A less. Customer B's deposit is credited — the bank owes B more. No cash leaves the building; total liabilities are unchanged because one liability fell while another rose by the same amount.",
    },
    {
      kind: "mc",
      id: "ch2-q7",
      difficulty: "challenge",
      concept: "double-entry",
      prompt:
        "A proposed ledger entry has $40 in debits and $30 in credits. What must a correct banking system do?",
      options: [
        "Reject the entry outright",
        "Post it and schedule a $10 correction for the next business day",
        "Silently add a $10 credit to balance the entry",
        "Accept it if the difference is under $100",
      ],
      answer: 0,
      explanation:
        "An unbalanced entry is not a minor discrepancy — it corrupts the accounting record. The [[double-entry]] invariant is enforced at posting time: if total debits ≠ total credits, the transaction is refused before it can touch any account.",
    },
    {
      kind: "mc",
      id: "ch2-q8",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "A $150 fee was posted to a customer account in error. What does the correcting reversal entry look like?",
      options: [
        "The mirror image: same $150 amounts, but debits become credits and credits become debits",
        "A second identical entry in the same debit/credit directions",
        "A deletion of the original entry from the ledger",
        "A write-off entry reducing Retained Earnings by $150",
      ],
      answer: 0,
      explanation:
        "A [[reversal]] posts the exact mirror of the original: every debit becomes a credit and every credit becomes a debit, at the same amounts. The two entries net to zero, returning balances to their pre-error state while preserving a full audit trail. Posted transactions are never deleted.",
    },
    {
      kind: "mc",
      id: "ch2-q9",
      difficulty: "challenge",
      concept: "normal-balance",
      prompt:
        "The bank's accountant debits the Equity account for $1,000 to record dividends paid to owners. What happens to equity?",
      options: [
        "Equity decreases by $1,000",
        "Equity increases by $1,000",
        "The entry is invalid — equity accounts can only be credited",
        "Equity is unchanged; only Retained Earnings changes",
      ],
      answer: 0,
      explanation:
        "Equity has a credit [[normal-balance]]: it grows with credits and shrinks with debits. A $1,000 debit to an equity account reduces it — this is exactly how dividend payments reduce owners' accumulated interest in the bank.",
    },
    {
      kind: "mc",
      id: "ch2-q10",
      difficulty: "core",
      concept: "amount-cents",
      prompt:
        "Banking ledgers store all monetary amounts as integer cents rather than decimal numbers. A customer deposits $1.05. How is this amount stored?",
      options: [
        "As the integer 105",
        "As the floating-point value 1.05",
        "As the integer 1, with 5 cents rounded off",
        "As the string \"1.05\"",
      ],
      answer: 0,
      explanation:
        "[[amount-cents]] — storing money as the integer count of the smallest currency unit — guarantees exact arithmetic. Floating-point types cannot represent every decimal fraction exactly, so rounding errors accumulate across millions of transactions. $1.05 = 105 cents.",
    },
    {
      kind: "truefalse",
      id: "ch2-q11",
      difficulty: "intro",
      prompt:
        "True or false: in double-entry bookkeeping, the word 'debit' always means a decrease to an account's balance.",
      answer: false,
      explanation:
        "'Debit' names the left side of an entry — it does not imply direction on its own. Whether a debit increases or decreases a balance depends on the account's [[normal-balance]]. A debit *increases* an asset but *decreases* a liability.",
    },
    {
      kind: "truefalse",
      id: "ch2-q12",
      difficulty: "intro",
      prompt:
        "True or false: a credit entry always increases the balance of the account it affects.",
      answer: false,
      explanation:
        "Credits increase liabilities, equity, and revenue — accounts with a credit [[normal-balance]] — but they *decrease* assets and expenses, which are debit-normal. The direction of effect always depends on account type.",
    },
    {
      kind: "truefalse",
      id: "ch2-q13",
      difficulty: "intro",
      prompt:
        "True or false: 'double-entry' means every accounting transaction has exactly two lines — one debit and one credit.",
      answer: false,
      explanation:
        "The 'double' refers to recording both perspectives — where value came from and where it went. A transaction may span three or more entries (e.g., a payment split across multiple fee accounts) as long as total debits equal total credits. See [[double-entry]].",
    },
    {
      kind: "truefalse",
      id: "ch2-q14",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "True or false: a reversal entry posts the identical debit/credit amounts and directions as the original transaction it corrects.",
      answer: false,
      explanation:
        "A [[reversal]] is the *mirror image* of the original: same amounts, but every debit becomes a credit and every credit becomes a debit. Posting the same directions a second time would *double* the effect rather than undo it.",
    },
    {
      kind: "multi",
      id: "ch2-q15",
      difficulty: "core",
      prompt:
        "Which of the following statements about double-entry bookkeeping are TRUE? (Select all that apply.)",
      options: [
        "Every transaction touches at least two accounts",
        "Total debits must equal total credits within every transaction",
        "A single transaction may span more than two entries",
        "A balanced trial balance guarantees no incorrect postings exist",
      ],
      answers: [0, 1, 2],
      explanation:
        "[[double-entry]] requires at least two balanced entries (A, B). Many real transactions — fee splits, multi-party payments — span three or more legs (C). Balance alone cannot detect a posting made to the *wrong but correctly typed* account, so D is false.",
    },
    {
      kind: "multi",
      id: "ch2-q16",
      difficulty: "core",
      concept: "account-type-expense",
      prompt:
        "The bank pays $400 in staff salaries in cash. Which TWO statements correctly describe this transaction? (Select all that apply.)",
      options: [
        "The Salary Expense account is debited",
        "The Cash account is credited",
        "The Salary Expense account is credited",
        "Total liabilities increase by $400",
      ],
      answers: [0, 1],
      explanation:
        "Expenses have a debit normal balance — debiting the [[account-type-expense]] records the cost (A). Cash is an asset (debit-normal), so crediting it reduces the bank's cash (B). Liabilities are unaffected: this converts one asset (cash) into an expense, not a debt (D is false).",
    },
    {
      kind: "multi",
      id: "ch2-q17",
      difficulty: "core",
      concept: "reversal",
      prompt:
        "An erroneous $200 interest charge is posted and then reversed. Which statements correctly describe the books after the reversal? (Select all that apply.)",
      options: [
        "The net effect on every account is zero",
        "Both the original entry and the reversal appear in the ledger",
        "The original entry is deleted from the ledger",
        "A $200 balance remains on the interest account until the next reconciliation",
      ],
      answers: [0, 1],
      explanation:
        "A [[reversal]] adds a new mirror-image journal entry — it does NOT delete the original. Both entries remain visible, preserving the audit trail (B). Because they are exact mirrors, their combined effect nets to zero on every account (A). D and C are false.",
    },
    {
      kind: "numeric",
      id: "ch2-q18",
      difficulty: "core",
      concept: "account-type-asset",
      prompt:
        "A customer deposits $500 cash. By how many dollars do the bank's total assets increase? (Enter a number of dollars.)",
      answer: 500,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The $500 cash deposit is recorded as a debit to Cash — an [[account-type-asset]] rises with a debit. The matching credit goes to the customer's deposit liability. Total assets grow by exactly $500; total liabilities also grow by $500, keeping the accounting equation balanced.",
    },
    {
      kind: "numeric",
      id: "ch2-q19",
      difficulty: "core",
      concept: "account-type-liability",
      prompt:
        "Customer A transfers $75 to Customer B — both banking at the same institution. By how many dollars does the bank's total liabilities change? (Enter a number of dollars.)",
      answer: 0,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Customer A's deposit (an [[account-type-liability]]) is debited $75; Customer B's deposit is credited $75. One liability falls while another rises by the same amount. The bank's total liabilities are unchanged — the obligation moved between customers, no money left the bank.",
    },
    {
      kind: "numeric",
      id: "ch2-q20",
      difficulty: "challenge",
      concept: "account-type-equity",
      prompt:
        "A bank earns $800 in net income during the year and pays no dividends. By how many dollars does equity increase when net income is closed to Retained Earnings at year-end? (Enter a number of dollars.)",
      answer: 800,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "At year-end, the net of Revenue minus Expense accounts is transferred into [[account-type-equity]] (Retained Earnings). With $800 net income and no dividends paid, equity grows by exactly $800. This is how profitable operations accumulate owner value on the balance sheet.",
    },
  ],
};
