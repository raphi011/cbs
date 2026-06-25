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
        "In a push payment scheme, which party's bank initiates the payment instruction?",
      options: [
        "The payee's bank (the bank receiving the money)",
        "The payer's bank (the bank sending the money)",
        "The central bank",
        "The card network",
      ],
      answer: 1,
      explanation:
        "In a [[scheme-direction-push]] scheme the **payer's bank** originates the instruction — the payer 'pushes' money to the payee. SEPA Credit Transfer is an example. Pull schemes work the opposite way.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch11-q2",
      difficulty: "intro",
      concept: "scheme-direction-pull",
      prompt:
        "Which scheme characteristic requires a mandate before a payment can be initiated?",
      options: [
        "Push payments",
        "Gross settlement",
        "Pull payments",
        "Instant payments",
      ],
      answer: 2,
      explanation:
        "[[scheme-direction-pull]] schemes let the **payee's bank** initiate the debit, so the payer must pre-authorise this via a [[mandate]]. Without a valid mandate the payment cannot proceed.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "truefalse",
      id: "ch11-q3",
      difficulty: "intro",
      concept: "settlement-model-net",
      prompt:
        "In a net settlement scheme, each payment is settled individually as soon as it is sent.",
      answer: false,
      explanation:
        "[[settlement-model-net]] batches payments into cycles. Positions across all participants are netted and only the **net differences** transfer at settlement time. This reduces total liquidity used compared to settling every payment individually.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "numeric",
      id: "ch11-q4",
      difficulty: "core",
      concept: "netting",
      unit: "dollars",
      prompt:
        "In a clearing cycle, Alice (Bank A) sends Bob (Bank B) $300, and Bob (Bank B) sends Alice (Bank A) $100. How many dollars actually move between reserves at settlement (net amount in dollars)?",
      answer: 200,
      explanation:
        "[[netting]] offsets obligations. Bank A owes $300, Bank B owes $100; they cancel to a **net $200** that Bank A pays to Bank B at settlement. Without netting $400 would flow in gross.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "truefalse",
      id: "ch11-q5",
      difficulty: "core",
      concept: "net-positions",
      prompt:
        "After netting, the sum of all participants' net positions always equals zero.",
      answer: true,
      explanation:
        "[[net-positions]] across all banks **must sum to zero** — every debit position is exactly offset by credit positions elsewhere. This is just double-entry applied at the system level.",
    },
    {
      kind: "mc",
      id: "ch11-q6",
      difficulty: "core",
      concept: "settlement-model-gross",
      prompt:
        "Which settlement model settles each payment individually and immediately, without netting?",
      options: [
        "Deferred Net Settlement (DNS)",
        "Real-Time Gross Settlement (RTGS)",
        "End-of-day batch settlement",
        "T+2 cycle settlement",
      ],
      answer: 1,
      explanation:
        "[[settlement-model-gross]] (RTGS) processes each payment one at a time, immediately. No netting occurs, so full liquidity is needed for every payment. SEPA Instant and FedNow are gross-settlement schemes.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "multi",
      id: "ch11-q7",
      difficulty: "core",
      concept: "settlement-delay",
      prompt:
        "Select ALL axes that distinguish one payment scheme from another.",
      options: [
        "Direction (push vs pull)",
        "Settlement model (net vs gross)",
        "Whether a mandate is required",
        "Whether returns are allowed",
        "Settlement delay (value date)",
        "The colour of the bank's logo",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "Payment schemes differ across five dimensions: [[scheme-direction-push]]/[[scheme-direction-pull]] direction, [[settlement-model-net]]/[[settlement-model-gross]] model, [[requires-mandate]], [[allows-return]], and [[settlement-delay]]. Logo colour is not a scheme parameter.",
      explore: { label: "Browse payment schemes", href: "/schemes" },
    },
    {
      kind: "mc",
      id: "ch11-q8",
      difficulty: "core",
      concept: "debtor-leg",
      prompt:
        "Alice sends €30 (3000 cents) to Bob via a credit transfer. At initiation, Bank A debits Alice's deposit. What is the offsetting credit?",
      options: [
        "Credit Bob's deposit at Bank B",
        "Credit Bank A's Reserve at the Central Bank",
        "Credit Bank A's Clearing Suspense account",
        "Credit Bank A's Equity",
      ],
      answer: 2,
      explanation:
        "The [[debtor-leg]] at initiation is: Debit Alice's deposit 3000 cents, Credit Clearing Suspense 3000 cents. Alice's money is now in transit; [[clearing-suspense]] holds it until settlement completes.",
    },
    {
      kind: "truefalse",
      id: "ch11-q9",
      difficulty: "challenge",
      concept: "scheme-direction-pull",
      prompt:
        "In a pull payment scheme, money still flows from debtor to creditor, even though the creditor's bank initiates the instruction.",
      answer: true,
      explanation:
        "The *direction of initiation* and the *direction of value* are independent. In [[scheme-direction-pull]] the creditor's bank sends the payment instruction, but the economic effect is always the same: funds move **from debtor to creditor**.",
    },
    {
      kind: "mc",
      id: "ch11-q10",
      difficulty: "challenge",
      concept: "settlement-model-net",
      prompt:
        "Bank A has sent 20 payments totalling $10,000 to other banks, and received 15 payments totalling $9,500 in one cycle. What is Bank A's net position at settlement?",
      options: [
        "Bank A pays $500 net",
        "Bank A receives $500 net",
        "Bank A pays $10,000 gross",
        "Bank A pays $19,500 gross",
      ],
      answer: 0,
      explanation:
        "[[settlement-model-net]]: Bank A owes $10,000 out and is owed $9,500 in. Net position = $10,000 − $9,500 = **$500 net outflow**. Only $500 of reserves transfer instead of $19,500 gross.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
  ],
};
