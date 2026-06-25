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
      prompt: "A customer deposits $500 cash at a bank. From the bank's perspective, the customer's deposit is:",
      options: [
        "An asset — the bank now holds the customer's money",
        "A liability — the bank owes the customer $500",
        "Revenue — the bank earned income",
        "Equity — the customer becomes a part-owner",
      ],
      answer: 1,
      explanation:
        "When you deposit money, the bank records it as a **[[account-type-liability]]**: it owes you that money back on demand. The cash held is the bank's asset; your deposit account is its debt.",
      explore: { label: "See the ledger", href: "/" },
    },
    {
      kind: "truefalse",
      id: "ch1-q2",
      difficulty: "intro",
      concept: "account-type-asset",
      prompt: "Customer deposits are assets on the bank's balance sheet.",
      answer: false,
      explanation:
        "Customer deposits are **liabilities**, not assets. The cash the bank holds is its [[account-type-asset]]; the obligation to return that cash to the customer is a [[account-type-liability]].",
    },
    {
      kind: "mc",
      id: "ch1-q3",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A customer deposits $500 cash. Which pair of entries correctly records this in the bank's books?",
      options: [
        "Debit Cash +$500 · Credit Customer Deposit Liability +$500",
        "Debit Customer Deposit Liability +$500 · Credit Cash +$500",
        "Debit Cash +$500 · Credit Revenue +$500",
        "Debit Equity +$500 · Credit Cash +$500",
      ],
      answer: 0,
      explanation:
        "[[double-entry]] bookkeeping requires every transaction to balance. Cash (an asset) increases on the debit side; the customer's deposit account (a liability) increases on the credit side. Both sides of the equation grow by $500.",
    },
    {
      kind: "mc",
      id: "ch1-q4",
      difficulty: "core",
      concept: "account-type-equity",
      prompt: "The accounting equation is: Assets = Liabilities + Equity. If a bank has $2,000,000 in assets and $1,800,000 in liabilities, what is equity?",
      options: [
        "$3,800,000",
        "$200,000",
        "$1,800,000",
        "$2,000,000",
      ],
      answer: 1,
      explanation:
        "Equity = Assets − Liabilities = $2,000,000 − $1,800,000 = **$200,000**. [[account-type-equity]] is the owners' residual stake after all debts are settled.",
    },
    {
      kind: "multi",
      id: "ch1-q5",
      difficulty: "core",
      concept: "account-type-asset",
      prompt: "Which of the following are assets on a bank's balance sheet? (Select all that apply.)",
      options: [
        "Cash held in vault",
        "Customer savings deposits",
        "Loans issued to borrowers",
        "Bonds issued by the bank",
        "Reserves held at the central bank",
      ],
      answers: [0, 2, 4],
      explanation:
        "[[account-type-asset]]s are things the bank owns or is owed: vault cash, loans it has extended (the borrower owes the bank), and reserves at the central bank. Customer deposits and bonds issued are **liabilities** — money the bank owes others.",
    },
    {
      kind: "truefalse",
      id: "ch1-q6",
      difficulty: "intro",
      concept: "double-entry",
      prompt:
        "When a customer deposits $500, the bank's total assets and total liabilities both increase by $500, leaving equity unchanged.",
      answer: true,
      explanation:
        "The deposit adds $500 to cash (asset) and $500 to the deposit liability. The equation Assets = Liabilities + Equity stays balanced because both sides increase equally. Equity is untouched.",
    },
    {
      kind: "mc",
      id: "ch1-q7",
      difficulty: "challenge",
      concept: "account-type-equity",
      prompt:
        "A bank issues a loan of $10,000 to a customer by crediting the customer's deposit account. Immediately after, what happens to the bank's balance sheet?",
      options: [
        "Assets increase by $10,000, liabilities increase by $10,000, equity unchanged",
        "Assets increase by $10,000, equity increases by $10,000, liabilities unchanged",
        "Assets unchanged, liabilities decrease by $10,000",
        "Equity decreases by $10,000 to fund the loan",
      ],
      answer: 0,
      explanation:
        "The bank gains a loan receivable (new [[account-type-asset]] +$10,000) and simultaneously creates a deposit for the customer (new [[account-type-liability]] +$10,000). Both sides of Assets = Liabilities + Equity expand equally; equity is unchanged.",
    },
    {
      kind: "mc",
      id: "ch1-q8",
      difficulty: "challenge",
      concept: "account-type-liability",
      prompt:
        "Why does storing records — not just balances — make a bank more trustworthy than a simple spreadsheet of balances?",
      options: [
        "Records allow the bank to detect errors, trace the reason for every balance change, and distinguish recorded credits from cash that has physically arrived",
        "Records compress data more efficiently, reducing storage costs",
        "A balance-only system is equally reliable; records are just an audit luxury",
        "Records allow the bank to charge higher fees because the system is more complex",
      ],
      answer: 0,
      explanation:
        "A ledger of immutable records lets the bank reconcile every balance change to a specific event, detect discrepancies between booked and settled amounts, and replay history. A balance-only store offers none of that auditability.",
      explore: { label: "See the ledger", href: "/" },
    },
    {
      kind: "truefalse",
      id: "ch1-q9",
      difficulty: "core",
      concept: "account-type-liability",
      prompt: "A bank's equity is reduced whenever a customer withdraws funds, because the bank has less cash.",
      answer: false,
      explanation:
        "A withdrawal reduces cash (asset) and reduces the deposit liability by the same amount. Both sides of the equation shrink equally, so equity is unchanged. Equity changes only when revenue or expenses arise, or when owners inject/withdraw capital.",
    },
    {
      kind: "multi",
      id: "ch1-q10",
      difficulty: "core",
      concept: "account-type-liability",
      prompt: "Which of the following are liabilities on a bank's balance sheet? (Select all that apply.)",
      options: [
        "Mortgage loans extended to homeowners",
        "Customer checking account balances",
        "Short-term borrowings from other banks",
        "Bonds the bank has issued to investors",
        "Securities held in the bank's investment portfolio",
      ],
      answers: [1, 2, 3],
      explanation:
        "[[account-type-liability]]s are what the bank owes: customer deposits, interbank borrowings, and bonds it has issued. Mortgage loans extended are assets (the borrower owes the bank); investment securities are also assets.",
    },
  ],
};
