import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "10-the-interbank-network",
  number: 10,
  part: "Part IV · Moving Money Between Banks",
  title: "The Interbank Network",
  questions: [
    {
      kind: "mc",
      id: "ch10-q1",
      difficulty: "intro",
      concept: "clearing-vs-settlement",
      prompt: "What is the difference between clearing and settlement?",
      options: [
        "Clearing exchanges and nets payment instructions; settlement moves reserves between banks at the central bank",
        "Clearing moves reserves; settlement exchanges payment instructions",
        "They are two names for the same process",
        "Clearing is for retail payments; settlement is for wholesale payments only",
      ],
      answer: 0,
      explanation:
        "**Clearing** is the exchange and netting of payment instructions — no money actually moves. **Settlement** is the moment reserves physically transfer between banks at the central bank, which is the moment of finality.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "mc",
      id: "ch10-q2",
      difficulty: "intro",
      concept: "reserve-account",
      prompt:
        "A bank needs three accounts beyond ordinary customer deposits to participate in interbank payments. Which of the following is one of them?",
      options: [
        "A reserve account held at the central bank (Asset)",
        "A correspondent account at a foreign currency exchange",
        "A revenue account for interchange fees",
        "An equity account for interbank capital",
      ],
      answer: 0,
      explanation:
        "The three accounts are: (1) customer deposits (Liability), (2) clearing suspense (Liability), and (3) [[reserve-account]] — the bank's claim on the central bank, which is an Asset.",
      explore: { label: "View central bank reserves", href: "/central-bank" },
    },
    {
      kind: "truefalse",
      id: "ch10-q3",
      difficulty: "intro",
      concept: "clearing-suspense",
      prompt:
        "The clearing suspense account is an Asset on the bank's balance sheet.",
      answer: false,
      explanation:
        "[[clearing-suspense]] is a **Liability**. It holds in-transit funds — money that has left the customer's deposit account but has not yet settled with the other bank. The bank still owes that value; it just hasn't yet received the offsetting reserve credit.",
    },
    {
      kind: "mc",
      id: "ch10-q4",
      difficulty: "core",
      concept: "clearing-suspense",
      prompt:
        "Alice (Bank A) sends $200 to Bob (Bank B). At the moment Alice's deposit is debited, which account is credited at Bank A?",
      options: [
        "Bank A's Reserve at Central Bank",
        "Bank A's Clearing Suspense account",
        "Bob's deposit at Bank B",
        "The Central Bank's Reserve: Bank A account",
      ],
      answer: 1,
      explanation:
        "Step 1 (initiation): Bank A debits Alice's deposit and credits its own [[clearing-suspense]] — a liability that captures funds in transit. Reserves don't move yet; that happens only at settlement.",
    },
    {
      kind: "truefalse",
      id: "ch10-q5",
      difficulty: "core",
      concept: "central-bank-reserves",
      prompt:
        "Bank A's 'Reserve at Central Bank' (Asset) and the Central Bank's 'Reserve: Bank A' (Liability) must always show the same balance.",
      answer: true,
      explanation:
        "These two entries are a **mirror image**: Bank A's asset is the central bank's liability for the same claim. Reconciliation means verifying that these mirror entries agree — they must always match.",
      explore: { label: "View central bank reserves", href: "/central-bank" },
    },
    {
      kind: "mc",
      id: "ch10-q6",
      difficulty: "core",
      concept: "reserve-account",
      prompt:
        "At settlement, Bank A sends $200 to Bank B for Alice's payment. From Bank A's perspective, which entry correctly describes the settlement?",
      options: [
        "Debit Clearing Suspense, Credit Reserve at Central Bank",
        "Debit Reserve at Central Bank, Credit Clearing Suspense",
        "Debit Alice's Deposit, Credit Reserve at Central Bank",
        "Debit Clearing Suspense, Credit Alice's Deposit",
      ],
      answer: 0,
      explanation:
        "At settlement, Bank A's [[clearing-suspense]] (the in-transit liability) is closed by debiting it, and the outgoing reserve transfer is recorded by crediting [[reserve-account]]. This reduces Bank A's claim on the central bank by $200.",
    },
    {
      kind: "truefalse",
      id: "ch10-q7",
      difficulty: "core",
      concept: "clearing-vs-settlement",
      prompt:
        "During the clearing phase, Bank A writes an entry directly into Bank B's ledger to record the pending payment.",
      answer: false,
      explanation:
        "Banks **never write in each other's ledgers**. During clearing, each bank updates only its own accounts. Settlement happens through the central bank, which debits one reserve account and credits another — the only shared ledger is the central bank's.",
    },
    {
      kind: "multi",
      id: "ch10-q8",
      difficulty: "challenge",
      concept: "central-bank-reserves",
      prompt:
        "Alice (Bank A) sends $200 to Bob (Bank B). Select ALL entries that occur at settlement — across all ledgers.",
      options: [
        "Central Bank: Debit Reserve:A, Credit Reserve:B",
        "Bank A: Debit Clearing Suspense, Credit Reserve at Central Bank",
        "Bank B: Debit Reserve at Central Bank, Credit Clearing Suspense",
        "Bank B: Debit Clearing Suspense, Credit Bob's Deposit",
        "Bank A: Debit Alice's Deposit, Credit Clearing Suspense",
      ],
      answers: [0, 1, 2, 3],
      explanation:
        "Settlement involves three ledgers: (1) The **Central Bank** debits Bank A's reserve and credits Bank B's reserve. (2) **Bank A** closes clearing suspense against its reserve asset. (3) **Bank B** records the incoming reserve and credits Bob. Option 4 is the *initiation* step (already done before clearing).",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "mc",
      id: "ch10-q9",
      difficulty: "challenge",
      concept: "clearing-suspense",
      prompt:
        "After settlement completes, what should the balance in Bank A's Clearing Suspense account be for Alice's $200 payment?",
      options: [
        "−$200 (a debit balance)",
        "$200 (a credit balance)",
        "$0 — it was opened and then fully closed",
        "It depends on whether Bank B has confirmed receipt",
      ],
      answer: 2,
      explanation:
        "[[clearing-suspense]] is a temporary in-transit account. It was credited $200 at initiation and debited $200 at settlement, so the net for this payment is **$0**. Any non-zero balance signals funds still in transit (unsettled).",
    },
    {
      kind: "mc",
      id: "ch10-q10",
      difficulty: "challenge",
      concept: "netting",
      prompt:
        "Bank A owes Bank B $500, and Bank B owes Bank A $300 from the same clearing cycle. After netting, how much do reserves actually move?",
      options: [
        "$800 — both gross amounts transfer",
        "$500 — Bank A's obligation is larger",
        "$200 — only the net difference transfers",
        "$0 — equal amounts cancel out",
      ],
      answer: 2,
      explanation:
        "[[netting]] reduces the total liquidity needed. The $300 Bank B owes Bank A offsets part of Bank A's obligation, leaving a **net of $200** flowing from Bank A to Bank B at settlement. This is the core efficiency gain of net settlement over gross settlement.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
  ],
};
