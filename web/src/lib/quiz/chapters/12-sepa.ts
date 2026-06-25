import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "12-sepa",
  number: 12,
  part: "Part IV · Moving Money Between Banks",
  title: "SEPA: Credit Transfers and Direct Debits",
  questions: [
    {
      kind: "mc",
      id: "ch12-q1",
      difficulty: "intro",
      concept: "scheme-direction-push",
      prompt: "SEPA Credit Transfer (SCT) is best described as which type of scheme?",
      options: [
        "A pull scheme requiring a mandate, settling in T+2",
        "A push scheme with no mandate required, settling in T+1",
        "A gross settlement scheme settling immediately",
        "A pull scheme with no mandate, settling in T+1",
      ],
      answer: 1,
      explanation:
        "[[scheme-direction-push]] — in SCT the **payer's bank** initiates the transfer. No [[mandate]] is needed because the payer is authorising the debit themselves. Settlement is T+1 (next business day).",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch12-q2",
      difficulty: "intro",
      concept: "requires-mandate",
      prompt: "Why does SEPA Direct Debit (SDD) require a mandate?",
      options: [
        "Because SDD is a gross-settlement scheme",
        "Because the payee's bank initiates the debit, so the payer must pre-authorise it",
        "Because SDD is always for amounts over €1,000",
        "Because the central bank requires mandates for all SEPA payments",
      ],
      answer: 1,
      explanation:
        "SDD is a [[scheme-direction-pull]] scheme — the **creditor's bank** initiates the debit on the debtor's account. A [[mandate]] (pre-authorisation) from the debtor is required before any debit can proceed.",
      explore: { label: "View mandates", href: "/mandates" },
    },
    {
      kind: "truefalse",
      id: "ch12-q3",
      difficulty: "intro",
      concept: "mandate",
      prompt:
        "Once a mandate passes all checks, the accounting postings for an SDD payment are different from those for an SCT payment.",
      answer: false,
      explanation:
        "Once mandate checks pass for SDD, the **postings are identical** to SCT: debit debtor leg, credit creditor leg through clearing suspense. The [[mandate]] check is a pre-condition gate, not a different accounting treatment.",
    },
    {
      kind: "multi",
      id: "ch12-q4",
      difficulty: "core",
      concept: "mandate",
      prompt:
        "When processing an SDD payment, which mandate checks must ALL pass before the payment can proceed?",
      options: [
        "Mandate exists",
        "Mandate is active (not revoked)",
        "Creditor on the mandate matches the initiating creditor",
        "Debtor on the mandate matches the account being debited",
        "Amount is within the mandate limit",
        "Payment currency is EUR",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "A [[mandate]] must pass five checks: it must exist, be active, creditor must match, debtor must match, and amount must be within the limit. Currency being EUR is a SEPA scheme-level requirement, not a mandate-level check in this model.",
      explore: { label: "View mandates", href: "/mandates" },
    },
    {
      kind: "mc",
      id: "ch12-q5",
      difficulty: "core",
      concept: "allows-return",
      prompt:
        "Which SEPA scheme allows a return ('R-transaction') to unwind a settled payment?",
      options: [
        "SEPA Credit Transfer (SCT) only",
        "SEPA Direct Debit (SDD) only",
        "Both SCT and SDD",
        "Neither — SEPA payments are always final",
      ],
      answer: 1,
      explanation:
        "[[allows-return]]: SDD supports R-transactions (returns) because a pull debit can be disputed. SCT does **not** allow returns *in this model* — here a settled credit transfer is treated as final. (Real SEPA does define SCT returns and recalls; this teaching model simply omits them.)",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "mc",
      id: "ch12-q6",
      difficulty: "core",
      concept: "payment-lifecycle",
      prompt:
        "Which ISO 20022 message type carries a SEPA Credit Transfer instruction?",
      options: [
        "pacs.003",
        "pacs.008",
        "pain.001",
        "camt.054",
      ],
      answer: 1,
      explanation:
        "The [[payment-lifecycle]] in SEPA uses ISO 20022 message types: **pacs.008** for SCT (credit transfer) and pacs.003 for SDD (direct debit). pain.001 is a customer-to-bank initiation message, not an interbank message.",
    },
    {
      kind: "truefalse",
      id: "ch12-q7",
      difficulty: "core",
      concept: "settlement-delay",
      prompt: "SEPA Direct Debit settles faster than SEPA Credit Transfer.",
      answer: false,
      explanation:
        "[[settlement-delay]]: SCT settles T+1, SDD settles **T+2** in this model. SDD is the slower of the two — a pull collection is tied to its mandate's due date, whereas a credit transfer is pushed straight through.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch12-q8",
      difficulty: "challenge",
      concept: "creditor-leg",
      prompt:
        "An SDD return is processed. The original payment had debited the debtor and credited the creditor. How does the return unwind the creditor's side?",
      options: [
        "The original posting is deleted from the ledger",
        "A new compensating transaction debits the creditor and credits clearing suspense",
        "The creditor's bank issues a pacs.008 to reverse",
        "The central bank voids the reserve transfer",
      ],
      answer: 1,
      explanation:
        "Returns follow the same append-only principle as reversals: a **new compensating transaction** is posted. The [[creditor-leg]] is debited (unwinding the credit), crediting clearing suspense, and the reserve flows back through settlement — no entries are deleted.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "mc",
      id: "ch12-q9",
      difficulty: "challenge",
      concept: "debtor-leg",
      prompt:
        "An SDD is rejected because the mandate was revoked before the payment date. Which error code is returned?",
      options: [
        "ErrMandateRequired",
        "ErrMandateRevoked",
        "ErrMandateExceeded",
        "ErrInsufficientFunds",
      ],
      answer: 1,
      explanation:
        "The [[debtor-leg]] check evaluates the mandate status. If the mandate exists but has been revoked, the error is **ErrMandateRevoked**. ErrMandateRequired fires when no mandate exists at all; ErrMandateExceeded fires when the amount exceeds the limit.",
      explore: { label: "View mandates", href: "/mandates" },
    },
    {
      kind: "truefalse",
      id: "ch12-q10",
      difficulty: "challenge",
      concept: "payment-lifecycle",
      prompt:
        "A SEPA Direct Debit can be initiated by the debtor's bank without a mandate from the debtor.",
      answer: false,
      explanation:
        "SDD is a pull scheme — the **creditor's bank** initiates the debit. A [[mandate]] signed by the debtor is a hard pre-condition. Without a valid, active mandate the payment is rejected before any accounting entries are made.",
      explore: { label: "View mandates", href: "/mandates" },
    },
  ],
};
