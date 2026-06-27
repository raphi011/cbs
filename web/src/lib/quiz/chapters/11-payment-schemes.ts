import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "11-payment-schemes",
  number: 11,
  part: "Part IV · Moving Money Between Banks",
  title: "Payment Schemes",
  questions: [
    {
      kind: "mc",
      id: "ch11-q1",
      difficulty: "intro",
      concept: "scheme-direction-push",
      prompt:
        "In a push payment, which party's bank originates the payment instruction?",
      options: [
        "The payer's bank (the bank sending the money)",
        "The payee's bank (the bank receiving the money)",
        "The central bank",
        "The card network",
      ],
      answer: 0,
      explanation:
        "In a [[scheme-direction-push]] payment the **payer's bank** originates the instruction — the payer 'pushes' funds toward the payee. SEPA Credit Transfer is the canonical example: the sender's bank submits the payment and funds flow out of the sender's account.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch11-q2",
      difficulty: "intro",
      concept: "scheme-direction-pull",
      prompt:
        "In a pull payment, which party's bank originates the payment instruction?",
      options: [
        "The payer's bank",
        "The payee's bank",
        "The central bank",
        "The clearing house",
      ],
      answer: 1,
      explanation:
        "In a [[scheme-direction-pull]] payment the **payee's bank** originates the instruction — the payee 'pulls' funds from the payer's account. SEPA Direct Debit works this way: the creditor's bank initiates the collection.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "truefalse",
      id: "ch11-q3",
      difficulty: "intro",
      concept: "requires-mandate",
      prompt:
        "In a pull scheme like SEPA Direct Debit, the payer must have signed a standing authorization (a mandate) before the payee's bank may collect funds.",
      answer: true,
      explanation:
        "Some pull schemes — including SEPA Direct Debit — make [[requires-mandate]] a hard rule: the payer must pre-authorize the relationship before the payee's bank may initiate a debit. Without an active mandate, the scheme rejects the payment before it ever reaches the clearing step.",
    },
    {
      kind: "mc",
      id: "ch11-q4",
      difficulty: "intro",
      concept: "settlement-model-net",
      prompt: "What does 'net settlement' mean in a payment scheme?",
      options: [
        "Every payment is settled individually and immediately as it is submitted",
        "Payments are batched into cycles, and only each bank's net position transfers at settlement",
        "All payments are settled directly at the central bank without a clearing step",
        "Settlement occurs at a fixed time each day with no position offsetting",
      ],
      answer: 1,
      explanation:
        "[[settlement-model-net]] groups payments into clearing cycles. At the cut-off, each bank's outflows and inflows are netted, and only the residual — the net position — moves as reserves. This dramatically reduces the liquidity banks must hold compared to gross settlement.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "truefalse",
      id: "ch11-q5",
      difficulty: "intro",
      concept: "net-positions",
      prompt:
        "After netting, the sum of all banks' net positions in a clearing cycle always equals zero.",
      answer: true,
      explanation:
        "[[net-positions]] across all participants must sum to zero — every dollar one bank owes net is a dollar another bank is owed net. This is double-entry applied at the system level: money is conserved; it only moves between participants.",
    },
    {
      kind: "mc",
      id: "ch11-q6",
      difficulty: "core",
      concept: "scheme-direction-push",
      prompt:
        "A customer logs into their bank app and initiates a transfer to pay their rent. Which scheme direction describes this payment?",
      options: [
        "Pull — the landlord's bank initiates the collection",
        "Push — the customer's bank originates the instruction and sends the funds",
        "Gross — because it settles immediately",
        "Net — because it batches with other payments",
      ],
      answer: 1,
      explanation:
        "The customer is voluntarily sending money out, so the **payer's bank** originates the instruction — the defining feature of a [[scheme-direction-push]] payment. The settlement model (net or gross) is a separate axis and has no bearing on push vs pull.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "numeric",
      id: "ch11-q7",
      difficulty: "core",
      concept: "netting",
      prompt:
        "In one clearing cycle, Bank A submits 4 outgoing payments of $75 each to Bank B, and Bank B submits 3 outgoing payments of $40 each to Bank A. How many dollars of central-bank reserves move at settlement?",
      answer: 180,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[netting]] aggregates all payments before computing net positions. Bank A's gross outflow = 4 × $75 = $300; Bank A's gross inflow = 3 × $40 = $120; net[A] = −$300 + $120 = −$180. Bank B's net = +$180. Only **$180** of reserves transfers — netting collapses the $420 gross total into a single $180 net flow.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "mc",
      id: "ch11-q8",
      difficulty: "core",
      concept: "debtor-leg",
      prompt:
        "Alice initiates a €30 credit transfer (3000 cents) from her account at Bank A to Bob at Bank B. What double-entry posting does Bank A record at initiation?",
      options: [
        "Debit Alice's deposit 3000 · Credit Bob's deposit 3000",
        "Debit Alice's deposit 3000 · Credit Clearing Suspense 3000",
        "Debit Clearing Suspense 3000 · Credit Alice's deposit 3000",
        "Debit Reserve at Central Bank 3000 · Credit Alice's deposit 3000",
      ],
      answer: 1,
      explanation:
        "The [[debtor-leg]] at initiation is: **Debit** Alice's deposit (her balance falls) and **Credit** Clearing Suspense (Bank A now owes the network). Alice's money sits in [[clearing-suspense]] — 'in flight' — until settlement moves reserves and the creditor leg is posted at Bank B.",
    },
    {
      kind: "mc",
      id: "ch11-q9",
      difficulty: "core",
      concept: "creditor-leg",
      prompt:
        "When the credit transfer from Alice to Bob finally settles, what posting does Bank B record to deliver the funds to Bob?",
      options: [
        "Debit Bob's deposit · Credit Clearing Suspense",
        "Debit Clearing Suspense · Credit Bob's deposit",
        "Debit Reserve at Central Bank · Credit Bob's deposit",
        "Debit Bob's deposit · Credit Reserve at Central Bank",
      ],
      answer: 1,
      explanation:
        "The [[creditor-leg]] at settlement is: **Debit** Clearing Suspense (releasing the in-flight funds) and **Credit** Bob's deposit (his balance rises). Bank B's Clearing Suspense, which held the incoming net position, clears back to zero.",
    },
    {
      kind: "mc",
      id: "ch11-q10",
      difficulty: "core",
      concept: "payment-lifecycle",
      prompt:
        "Which lifecycle state does a payment enter immediately after the debtor leg is posted?",
      options: ["Initiated", "Accepted", "Cleared", "Settled"],
      answer: 1,
      explanation:
        "The [[payment-lifecycle]] state machine is Initiated → **Accepted** → Cleared → Settled. The [[debtor-leg]] is posted on the Initiated → Accepted transition, once the scheme validates the payment (sufficient funds, mandate active if required). The payment reaches Cleared only later, at the cut-off when net positions are computed.",
    },
    {
      kind: "truefalse",
      id: "ch11-q11",
      difficulty: "core",
      concept: "scheme-direction-pull",
      prompt:
        "In a pull payment, money flows from the creditor's account to the debtor's account — the reverse of a push payment.",
      answer: false,
      explanation:
        "Money always flows **debtor → creditor**, regardless of who initiates. In a [[scheme-direction-pull]] scheme the creditor's bank *initiates* the instruction, but the economic direction of value is identical to a push: funds leave the payer (debtor) and arrive at the payee (creditor).",
    },
    {
      kind: "multi",
      id: "ch11-q12",
      difficulty: "core",
      concept: "allows-return",
      prompt:
        "Which of the following are properties of SEPA Direct Debit (SDD) but NOT of SEPA Credit Transfer (SCT)?",
      options: [
        "The payee's bank originates the payment instruction",
        "The payer must have signed a mandate before funds can be collected",
        "The payer may demand a return after the payment has settled",
        "Money flows from the creditor's account to the debtor's account",
        "Each payment settles individually without netting",
      ],
      answers: [0, 1, 2],
      explanation:
        "SDD is [[scheme-direction-pull]] — the payee's bank initiates. It [[requires-mandate]] (a signed authorization). And it [[allows-return]] — the debtor can dispute and reclaim funds after settlement via a SEPA R-transaction. SCT is push, requires no mandate, and does not define a return right. Both schemes use net settlement — not individual (gross) settlement.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch11-q13",
      difficulty: "core",
      concept: "clearing-suspense",
      prompt:
        "Between the moment Alice's debit is posted at initiation and the moment Bob's credit is posted at settlement, where does Alice's money 'live' in Bank A's ledger?",
      options: [
        "In Bob's deposit account at Bank B",
        "In the central bank's reserve account",
        "In Bank A's Clearing Suspense account",
        "It is removed from the ledger until settlement",
      ],
      answer: 2,
      explanation:
        "[[clearing-suspense]] is the 'in-flight' liability account that holds funds between the [[debtor-leg]] and final settlement. Debit Alice → Credit Clearing Suspense parks the money there. At settlement, Clearing Suspense is debited as Bank A's reserve asset falls — the suspense clears to zero.",
    },
    {
      kind: "numeric",
      id: "ch11-q14",
      difficulty: "core",
      concept: "settlement-model-net",
      prompt:
        "Bank A sends $10,000 in payments to other banks and receives $9,500 from other banks in one clearing cycle. How many dollars does Bank A pay out at settlement?",
      answer: 500,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[settlement-model-net]] computes the net position: $10,000 outgoing − $9,500 incoming = **$500 net outflow**. Bank A's reserve account at the central bank decreases by only $500, not by the $10,000 gross outgoing total.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "mc",
      id: "ch11-q15",
      difficulty: "core",
      concept: "payment-lifecycle",
      prompt:
        "A payment has been accepted but has not yet reached the clearing cut-off. The scheme then rejects it due to a validation failure. What happens to the debtor leg that was already posted?",
      options: [
        "It is left as-is; the payee's bank issues a refund separately",
        "It is reversed, restoring the payer's account balance",
        "It moves to Returned state and waits for the next R-cycle",
        "It is cancelled automatically at the clearing cut-off",
      ],
      answer: 1,
      explanation:
        "The [[payment-lifecycle]] state for a failed payment before clearing is **Rejected**. At rejection, the debtor-leg posting is reversed — a compensating entry credits the payer's account back and clears Clearing Suspense to zero. The **Returned** state is distinct: it applies *after* a payment has already settled.",
    },
    {
      kind: "truefalse",
      id: "ch11-q16",
      difficulty: "challenge",
      concept: "settlement-model-gross",
      prompt:
        "UK Faster Payments is a gross settlement scheme because customers experience near-instant transfers.",
      answer: false,
      explanation:
        "[[settlement-model-gross]] means each payment settles individually and immediately at the central bank. UK Faster Payments *feels* instant to customers but actually settles on a **deferred net basis** behind the scenes. Instant customer experience and gross settlement are independent properties — a scheme can be one without the other.",
    },
    {
      kind: "multi",
      id: "ch11-q17",
      difficulty: "challenge",
      concept: "settlement-delay",
      prompt:
        "The scheme model defines a fixed set of axes along which payment products differ. Select ALL that are genuine scheme axes.",
      options: [
        "Direction: which party's bank initiates (push vs pull)",
        "Settlement model: net (batched and netted) vs gross (per-payment, immediate)",
        "Whether a mandate is required before funds may be collected",
        "Whether settled payments may be returned within a window",
        "How long after booking the payment takes economic effect (the value date / settlement delay)",
        "Which ISO 20022 message format the scheme uses internally",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "The scheme interface captures exactly five axes: [[scheme-direction-push]]/[[scheme-direction-pull]] direction, [[settlement-model-net]]/[[settlement-model-gross]] model, [[requires-mandate]], [[allows-return]], and [[settlement-delay]]. ISO 20022 message names (pacs.008, pacs.003) are implementation labels, not scheme-differentiating axes.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch11-q18",
      difficulty: "challenge",
      concept: "requires-mandate",
      prompt:
        "A utility company attempts to collect funds from a customer via a pull payment scheme, but no mandate has been signed. What is the correct outcome?",
      options: [
        "The payment proceeds but is flagged for manual review",
        "The payment is rejected before the debtor leg is posted",
        "The payment settles, but the customer can demand a return",
        "The payment clears normally because the bank can waive the mandate requirement",
      ],
      answer: 1,
      explanation:
        "[[requires-mandate]] schemes validate the mandate at the Initiated → Accepted step. If no valid mandate exists, the scheme rejects the payment with an error (such as `ErrMandateRequired`) and **no debtor leg is ever posted**. This protects payers from unauthorized debits before any money moves.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "multi",
      id: "ch11-q19",
      difficulty: "challenge",
      concept: "creditor-leg",
      prompt:
        "Select ALL statements that accurately describe the creditor leg of an interbank payment.",
      options: [
        "It is posted when the payment transitions from Cleared to Settled",
        "It debits the payer's deposit account",
        "It credits the payee's deposit account at the receiving bank",
        "It is posted at the same moment as the debtor leg, when the payment is initiated",
        "It releases funds from the receiving bank's Clearing Suspense into the payee's account",
      ],
      answers: [0, 2, 4],
      explanation:
        "The [[creditor-leg]] is posted at **settlement** (Cleared → Settled), not at initiation. It consists of: Debit Clearing Suspense (releasing the in-flight funds) → Credit payee's deposit. This is distinct from the [[debtor-leg]], which debits the payer's deposit and credits the *sending* bank's Clearing Suspense at initiation.",
    },
    {
      kind: "numeric",
      id: "ch11-q20",
      difficulty: "challenge",
      concept: "settlement-model-gross",
      prompt:
        "Bank A sends Bank B $300 and Bank B sends Bank A $100 in one clearing cycle. How many dollars of reserves would move if these payments settled gross (individually) instead of net?",
      answer: 400,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Under [[settlement-model-gross]] every payment settles individually: $300 moves one way and $100 moves the other — **$400** of reserves in total. Netting collapses this to just $200, which is exactly why clearing exists as a step before settlement: it converts a large gross flow into a small net one.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
  ],
};
