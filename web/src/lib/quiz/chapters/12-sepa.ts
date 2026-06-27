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
      prompt: "Which statement best describes SEPA Credit Transfer (SCT)?",
      options: [
        "A push scheme — the payer's bank initiates and no mandate is required",
        "A pull scheme — the payee's bank initiates and a mandate is required",
        "A push scheme — the payee's bank initiates and a mandate is required",
        "A pull scheme — the payer's bank initiates and no mandate is required",
      ],
      answer: 0,
      explanation:
        "[[scheme-direction-push]] — in SCT the payer's bank initiates and pushes funds to the payee. No [[mandate]] is required because the payer is authorising the debit themselves. It settles at T+1 via the ISO 20022 `pacs.008` interbank message.",
    },
    {
      kind: "mc",
      id: "ch12-q2",
      difficulty: "intro",
      concept: "requires-mandate",
      prompt:
        "What is the core reason SEPA Direct Debit (SDD) requires a mandate before any collection?",
      options: [
        "SDD is a gross-settlement scheme that requires pre-authorisation",
        "The creditor's bank reaches into the debtor's account, so the debtor must pre-authorise it",
        "The central bank mandates pre-authorisation for all SEPA pull payments",
        "SDD always requires the payer's bank to confirm the instruction before debiting",
      ],
      answer: 1,
      explanation:
        "SDD is a [[scheme-direction-pull]] — the creditor initiates the debit on the debtor's account. A [[mandate]] is the standing authorisation the debtor signs in advance, naming the specific creditor and often capping the amount. Without a valid mandate the collection is rejected before any posting occurs.",
      explore: { label: "View mandates", href: "/mandates" },
    },
    {
      kind: "mc",
      id: "ch12-q3",
      difficulty: "intro",
      concept: "scheme-direction-pull",
      prompt:
        "Which ISO 20022 interbank message type carries a SEPA Direct Debit instruction between banks?",
      options: [
        "pacs.008",
        "pain.001",
        "camt.054",
        "pacs.003",
      ],
      answer: 3,
      explanation:
        "[[scheme-direction-pull]] — SDD is a pull scheme whose interbank message is **pacs.003**. The SCT counterpart (a push) uses `pacs.008`. The other two options are distractors; the model identifies only `pacs.008` (SCT) and `pacs.003` (SDD) as the SEPA interbank messages.",
    },
    {
      kind: "mc",
      id: "ch12-q4",
      difficulty: "core",
      concept: "mandate",
      prompt:
        "An SDD collection arrives but the debtor revoked the mandate two weeks before the due date. Which specific error does the scheme return?",
      options: [
        "ErrMandateRequired",
        "ErrMandateRevoked",
        "ErrMandateExceeded",
        "ErrInsufficientFunds",
      ],
      answer: 1,
      explanation:
        "The [[mandate]] status check fires before any posting. A mandate that exists but has been revoked triggers **ErrMandateRevoked**. `ErrMandateRequired` fires when no mandate exists at all; `ErrMandateExceeded` fires when the collection amount exceeds the mandate limit.",
      explore: { label: "View mandates", href: "/mandates" },
    },
    {
      kind: "mc",
      id: "ch12-q5",
      difficulty: "core",
      concept: "allows-return",
      prompt:
        "SEPA defines a no-questions-asked refund that a debtor may claim after a settled direct debit. Within what window must this refund be requested?",
      options: [
        "2 weeks",
        "4 weeks",
        "8 weeks",
        "13 weeks",
      ],
      answer: 2,
      explanation:
        "[[allows-return]] — SEPA's R-transaction family includes both *returns* (dispute or failed funds) and *refunds*. The **refund** gives the debtor an **8-week**, no-questions-asked window to reclaim any settled SDD collection. After that window a dispute must be substantiated; it is no longer automatic.",
    },
    {
      kind: "mc",
      id: "ch12-q6",
      difficulty: "core",
      concept: "debtor-leg",
      prompt:
        "In a SEPA Credit Transfer, at which stage of the lifecycle does the debtor leg post — the entry that moves funds out of the payer's account?",
      options: [
        "At clearing, when net positions are computed",
        "At initiation, when the payment is accepted by the scheme",
        "At settlement, when central-bank reserves move",
        "At mandate validation, before any other checks",
      ],
      answer: 1,
      explanation:
        "The [[debtor-leg]] — the entry debiting the payer's deposit account and crediting the bank's clearing suspense — posts at **initiation/acceptance**, value-dated to the settlement date. Clearing later computes net positions; settlement posts the creditor leg. No debit to the customer's account occurs at clearing itself.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "mc",
      id: "ch12-q7",
      difficulty: "core",
      concept: "creditor-leg",
      prompt:
        "In a SEPA payment, when does the creditor leg post — the entry that delivers funds into the payee's account?",
      options: [
        "At initiation, alongside the debtor leg",
        "At clearing, when the net position is finalised",
        "At settlement, after central-bank reserves have moved",
        "Immediately on receipt of the pacs.008 or pacs.003 message",
      ],
      answer: 2,
      explanation:
        "The [[creditor-leg]] posts at **settlement**: only after central-bank reserves have moved from the debtor's bank to the creditor's bank does the creditor's bank credit the payee's deposit account (and debit its own reserve asset). The debtor leg posts earlier, at initiation, into the initiating bank's clearing suspense.",
    },
    {
      kind: "mc",
      id: "ch12-q8",
      difficulty: "core",
      concept: "clearing-vs-settlement",
      prompt:
        "What is the defining difference between the 'Cleared' state and the 'Settled' state in the SEPA payment lifecycle?",
      options: [
        "In 'Cleared', the creditor leg has posted; in 'Settled', the debtor leg has also posted",
        "In 'Cleared', net positions are computed but no central-bank reserves have moved; in 'Settled', reserves have moved and the creditor leg has posted",
        "In 'Cleared', the payment is final and irrevocable; in 'Settled', the creditor leg is still pending",
        "In 'Cleared', both banks have confirmed receipt; in 'Settled', the central bank has confirmed",
      ],
      answer: 1,
      explanation:
        "[[clearing-vs-settlement]] — clearing computes the **net** amounts each bank owes, but no central-bank money moves yet. Settlement is the moment of finality: reserves transfer between banks at the central bank and the [[creditor-leg]] posts, delivering funds to the payee. Only after settlement can a return unwind the flow.",
    },
    {
      kind: "mc",
      id: "ch12-q9",
      difficulty: "challenge",
      concept: "mandate",
      prompt:
        "A creditor submits an SDD for €450 against a mandate capped at €400. The mandate exists, is active, and both parties match. What does the scheme do?",
      options: [
        "Processes the payment — the cap is advisory when all other checks pass",
        "Rejects with ErrMandateExceeded before any posting",
        "Approves €400 and queues the €50 excess for the next cycle",
        "Automatically updates the mandate cap to €450 and proceeds",
      ],
      answer: 1,
      explanation:
        "The [[mandate]] amount cap is a hard gate, not advisory. Even when every other mandate check passes, a collection exceeding the mandate limit is rejected with **ErrMandateExceeded** before any posting occurs. There is no partial approval or automatic cap increase.",
      explore: { label: "View mandates", href: "/mandates" },
    },
    {
      kind: "mc",
      id: "ch12-q10",
      difficulty: "challenge",
      concept: "payment-lifecycle",
      prompt:
        "After an SDD reaches the 'Settled' state, the debtor disputes the collection. Which state does the payment transition to?",
      options: [
        "Rejected — the payment is unwound before it becomes final",
        "Cleared — the payment re-enters the netting queue",
        "Returned — compensating R-transaction entries are posted to reverse the flow",
        "Cancelled — both banks delete the original postings",
      ],
      answer: 2,
      explanation:
        "The [[payment-lifecycle]] has a 'Returned' terminal state, reachable *only* after settlement via a SEPA R-transaction. A return is not an undo of the lifecycle — it posts new compensating entries that restore both balances while leaving the original entries intact in the ledger. 'Rejected' is only reachable before settlement.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "truefalse",
      id: "ch12-q11",
      difficulty: "intro",
      concept: "scheme-direction-pull",
      prompt:
        "SEPA Direct Debit (SDD) is a push payment — the debtor's bank initiates the transfer to the creditor.",
      answer: false,
      explanation:
        "SDD is a [[scheme-direction-pull]] payment. The **creditor's** bank initiates the collection from the debtor's account. Because the creditor is reaching into someone else's account, a signed [[mandate]] is required before any debit can proceed.",
    },
    {
      kind: "truefalse",
      id: "ch12-q12",
      difficulty: "core",
      concept: "payment-lifecycle",
      prompt:
        "Once mandate checks pass for an SDD, the accounting postings follow a different path from an SCT — the pull direction requires separate posting logic.",
      answer: false,
      explanation:
        "Once the mandate gate clears, SDD uses **exactly the same posting engine** as SCT: debtor → clearing suspense → central-bank reserves → creditor. This is a key insight of the [[payment-lifecycle]]: only the rules governing initiation and authorisation differ — the underlying ledger choreography is shared by both schemes.",
    },
    {
      kind: "truefalse",
      id: "ch12-q13",
      difficulty: "core",
      concept: "settlement-delay",
      prompt:
        "In this model, SEPA Direct Debit settles one business day after initiation (T+1), the same as SEPA Credit Transfer.",
      answer: false,
      explanation:
        "[[settlement-delay]] — this model settles SDD at **T+2**, not T+1. SCT settles at T+1. The longer SDD delay reflects that a pull collection is tied to a due date; real SDD Core works similarly, though it is driven by the mandate's due date rather than a fixed two-day offset.",
    },
    {
      kind: "truefalse",
      id: "ch12-q14",
      difficulty: "challenge",
      concept: "allows-return",
      prompt:
        "When a SEPA return is processed, the original settled ledger entries are deleted from both banks' books to restore the pre-settlement state.",
      answer: false,
      explanation:
        "Ledgers are immutable — entries are never deleted. [[allows-return]] — a return works like a [[reversal]] across banks: new **compensating transactions** are posted that exactly offset the original flow, leaving net balances at zero. Both the original and compensating entries remain permanently in the ledger history.",
    },
    {
      kind: "multi",
      id: "ch12-q15",
      difficulty: "core",
      concept: "mandate",
      prompt:
        "When processing an SDD, which mandate checks must ALL pass before the payment can proceed? (Select all that apply.)",
      options: [
        "Mandate exists in the system",
        "Mandate is still active (not revoked)",
        "Creditor on the mandate matches the initiating creditor",
        "Debtor on the mandate matches the account being debited",
        "Collection amount is within the mandate's limit",
        "Mandate was signed within the last 12 months",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "A [[mandate]] must pass five checks before any SDD proceeds: existence, active status, creditor match, debtor match, and amount within limit. A signature recency requirement is not one of the checks in this model — only the five checks above are enforced.",
    },
    {
      kind: "multi",
      id: "ch12-q16",
      difficulty: "core",
      concept: "payment-lifecycle",
      prompt:
        "Which of the following are states on the successful (non-rejected, non-returned) path through the SEPA payment state machine? (Select all that apply.)",
      options: [
        "Initiated",
        "Pending",
        "Accepted",
        "Cleared",
        "Settled",
        "Completed",
      ],
      answers: [0, 2, 3, 4],
      explanation:
        "The [[payment-lifecycle]] successful path runs: **Initiated → Accepted → Cleared → Settled**. 'Pending' and 'Completed' are not named states in this model. The full state machine also includes 'Rejected' (reachable after Accepted) and 'Returned' (reachable after Settled) as terminal states for failed or disputed payments.",
    },
    {
      kind: "multi",
      id: "ch12-q17",
      difficulty: "challenge",
      concept: "clearing-vs-settlement",
      prompt:
        "Which of the following events occur at settlement — not during clearing or at initiation? (Select all that apply.)",
      options: [
        "Net positions are computed from all payment instructions in the cycle",
        "Central-bank reserves move between participant banks",
        "The creditor leg posts, delivering funds into the payee's account",
        "The debtor leg posts, moving the payer's money into clearing suspense",
        "Both banks' clearing suspense accounts return to zero",
      ],
      answers: [1, 2, 4],
      explanation:
        "[[clearing-vs-settlement]] — option A (net positions computed) happens at clearing; option D (debtor leg) happens at initiation. At settlement: **reserves move at the central bank** (B), the **creditor leg posts** at the receiving bank (C), and **clearing suspense balances zero out** across both banks (E), completing the flow.",
    },
    {
      kind: "numeric",
      id: "ch12-q18",
      difficulty: "intro",
      concept: "debtor-leg",
      prompt:
        "Alice initiates a €75 SEPA Credit Transfer to Bob. The debtor leg posts at initiation. By how many euros does Alice's book balance decrease? (Enter a number.)",
      answer: 75,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "The [[debtor-leg]] at initiation debits Alice's deposit account (a Liability) by **€75**, reducing her book balance immediately, while crediting the bank's clearing suspense. The full €75 leaves Alice's balance at initiation — before clearing or settlement occur.",
    },
    {
      kind: "numeric",
      id: "ch12-q19",
      difficulty: "core",
      concept: "clearing-vs-settlement",
      prompt:
        "In one clearing cycle: Bank A sends $40 to Bank B, and Bank B sends $15 to Bank A. How many dollars of central-bank reserves actually move at settlement? (Enter a number.)",
      answer: 25,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[clearing-vs-settlement]] — netting is the whole point of clearing. Bank A's net = −$40 + $15 = −$25; Bank B's net = +$40 − $15 = +$25. Only **$25** of central-bank reserves transfers at settlement, not the gross $55. Net positions always sum to zero, which is exactly why the settlement transaction itself balances.",
    },
    {
      kind: "numeric",
      id: "ch12-q20",
      difficulty: "challenge",
      concept: "allows-return",
      prompt:
        "A $200 SDD settles successfully. The debtor then triggers a return. After the compensating return transactions post, by how many dollars does the creditor's book balance decrease? (Enter a number.)",
      answer: 200,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[allows-return]] — a return fully unwinds the original settlement. The creditor received $200 when the [[creditor-leg]] posted at settlement; the return posts new compensating entries that exactly reverse that credit, reducing the creditor's balance by **$200**. The original entries remain in the ledger — only new offsetting entries are added.",
    },
  ],
};
