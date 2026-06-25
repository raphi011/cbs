import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "05-transactions-and-postings",
  number: 5,
  part: "Part II · Transactions and Time",
  title: "Transactions and Postings",
  questions: [
    {
      kind: "mc",
      id: "ch5-q1",
      difficulty: "intro",
      concept: "double-entry",
      prompt: "A transaction is described as a 'balanced set of entries posted atomically.' What does 'balanced' mean in this context?",
      options: [
        "The transaction touches the same number of debit and credit accounts",
        "The total debits equal the total credits across all legs of the transaction",
        "The transaction must involve exactly two accounts",
        "Balanced means the ledger has been audited and confirmed correct",
      ],
      answer: 1,
      explanation:
        "[[double-entry]] bookkeeping requires every transaction to balance: **total debits = total credits**. The number of legs can vary (a transaction can have three or more legs), but the sum of debits must equal the sum of credits.",
    },
    {
      kind: "truefalse",
      id: "ch5-q2",
      difficulty: "intro",
      concept: "reversal",
      prompt: "To correct an incorrect posted transaction, you should delete or edit it in the ledger.",
      answer: false,
      explanation:
        "The ledger is **immutable and append-only** — posted transactions are never edited or deleted. The correct method is a **[[reversal]]**: a new transaction in which every debit becomes a credit and vice versa, cancelling the original entry.",
    },
    {
      kind: "mc",
      id: "ch5-q3",
      difficulty: "intro",
      concept: "idempotency-key",
      prompt: "Why does a payment API accept an idempotency key from the caller?",
      options: [
        "To encrypt the payment and protect against fraud",
        "To prevent duplicate postings if the same request is retried after a network failure",
        "To compress the request payload for faster transmission",
        "To link the payment to a specific bank branch",
      ],
      answer: 1,
      explanation:
        "An [[idempotency-key]] is a unique ID (UUID) the caller assigns to a logical operation. If the network fails and the caller retries, the server recognises the key and returns the original result instead of posting a duplicate transaction.",
    },
    {
      kind: "mc",
      id: "ch5-q4",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "A sender pays $100 to a recipient, and the bank charges a $2 fee. How many legs does this transaction have?",
      options: ["1", "2", "3", "4"],
      answer: 2,
      explanation:
        "Three legs: (1) debit sender's account −$102, (2) credit recipient's account +$100, (3) credit Fee Income +$2. [[double-entry]] allows more than two legs as long as total debits equal total credits ($102 = $100 + $2).",
    },
    {
      kind: "truefalse",
      id: "ch5-q5",
      difficulty: "core",
      concept: "double-entry",
      prompt: "A trial balance proves that all transactions were posted correctly, with no errors.",
      answer: false,
      explanation:
        "A trial balance only verifies that **total debits equal total credits** — it is consistent with [[double-entry]] rules. It does NOT detect compensating errors (e.g., debiting the wrong account with the right amount), or entries posted to wrong accounts.",
    },
    {
      kind: "mc",
      id: "ch5-q6",
      difficulty: "core",
      concept: "reversal",
      prompt: "A $500 payment was posted to the wrong customer's account. What is the correct procedure?",
      options: [
        "Update the original transaction's account field in the database",
        "Delete the original posting and re-post to the correct account",
        "Post a reversal transaction (all debits become credits, vice versa), then post a new correct transaction",
        "Post an additional transaction to the correct account without reversing the error",
      ],
      answer: 2,
      explanation:
        "The ledger is immutable. The proper fix is a **[[reversal]]** — a new transaction that mirror-images the original to net it to zero — followed by a new correct transaction. This preserves the full audit trail.",
    },
    {
      kind: "multi",
      id: "ch5-q7",
      difficulty: "core",
      concept: "idempotency-key",
      prompt: "Which of the following are true about idempotency keys? (Select all that apply.)",
      options: [
        "They are unique UUIDs assigned per logical operation",
        "If the same key is submitted twice, the second call returns the original result without a new posting",
        "They must be regenerated on every retry to ensure uniqueness",
        "They protect against duplicate transactions caused by network retries",
        "They replace the need for double-entry bookkeeping",
      ],
      answers: [0, 1, 3],
      explanation:
        "[[idempotency-key]]: a UUID assigned once per logical operation. Retrying with the **same** key is safe — the server deduplicates. Regenerating the key on retry would defeat the purpose, creating a duplicate. Idempotency keys are unrelated to [[double-entry]].",
    },
    {
      kind: "mc",
      id: "ch5-q8",
      difficulty: "challenge",
      concept: "double-entry",
      prompt:
        "A bank's trial balance shows total debits of $4,200,000 and total credits of $3,900,000. What does this indicate?",
      options: [
        "The bank is profitable — debits exceed credits",
        "The ledger has an error: an unbalanced transaction was posted, or a posting was missed",
        "The bank has more assets than liabilities, which is normal and expected",
        "Some accounts have not been closed at year-end yet",
      ],
      answer: 1,
      explanation:
        "A trial balance must balance: total debits = total credits. A $300,000 discrepancy means at least one [[double-entry]] transaction is unbalanced — either an entry is missing, incorrectly posted, or a figure was transposed. This requires investigation.",
    },
    {
      kind: "truefalse",
      id: "ch5-q9",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "A reversal transaction must be posted with the same date as the original transaction it cancels.",
      answer: false,
      explanation:
        "A **[[reversal]]** is a *new* transaction appended to the ledger — it carries its own posting date (typically the current date). The original transaction remains in history unchanged. Backdating is generally not permitted in an immutable ledger.",
    },
    {
      kind: "mc",
      id: "ch5-q10",
      difficulty: "challenge",
      concept: "idempotency-key",
      prompt:
        "A mobile payment app sends a $50 transfer. The server processes it but the response is lost in transit. The app retries with the same idempotency key. What should happen?",
      options: [
        "The server posts a second $50 transfer — the first may not have succeeded",
        "The server recognises the key, skips re-processing, and returns the original response",
        "The server rejects the retry as a suspected replay attack",
        "The server posts a compensating reversal and then a new transfer",
      ],
      answer: 1,
      explanation:
        "This is the core purpose of [[idempotency-key]]: the server already committed the first transaction. Seeing the same key, it returns the stored result without posting again — the customer pays exactly once regardless of network failures.",
      explore: { label: "See payments", href: "/payments" },
    },
  ],
};
