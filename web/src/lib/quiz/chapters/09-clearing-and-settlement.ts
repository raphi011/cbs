import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "09-clearing-and-settlement",
  number: 9,
  part: "Part IV · Moving Money Between Banks",
  title: "Clearing and Settlement",
  questions: [
    {
      kind: "mc",
      id: "ch9-q1",
      difficulty: "intro",
      concept: "clearing-vs-settlement",
      prompt: "What is the key difference between clearing and settlement?",
      options: [
        "Clearing moves actual money; settlement only agrees on amounts",
        "Clearing agrees on who owes whom; settlement is the actual transfer of value that creates finality",
        "Clearing happens at the central bank; settlement happens between commercial banks",
        "They are different names for the same process",
      ],
      answer: 1,
      explanation:
        "[[clearing-vs-settlement|Clearing]] is information exchange — banks reconcile payment instructions and agree on net positions. [[clearing-vs-settlement|Settlement]] is the moment reserves actually move and finality is achieved. No money moves during clearing.",
    },
    {
      kind: "mc",
      id: "ch9-q2",
      difficulty: "intro",
      concept: "payment-lifecycle",
      prompt:
        "In which phase does a bank debit the customer's account and create a payment instruction?",
      options: ["Clearing", "Settlement", "Initiation", "Netting"],
      answer: 2,
      explanation:
        "The [[payment-lifecycle|Initiation]] phase is where the sending bank debits the customer and generates the payment instruction. The instruction then enters the [[clearing-vs-settlement|clearing]] process before [[clearing-vs-settlement|settlement]] moves the interbank reserves.",
    },
    {
      kind: "truefalse",
      id: "ch9-q3",
      difficulty: "intro",
      concept: "netting",
      prompt:
        "During clearing, each individual payment is settled one-by-one in real time.",
      answer: false,
      explanation:
        "Clearing calculates [[netting|net positions]] across many payments — banks offset credits against debits so that only the net difference needs to settle. This reduces the volume of reserve transfers. Gross settlement (RTGS) is a different model where each payment settles individually.",
    },
    {
      kind: "mc",
      id: "ch9-q4",
      difficulty: "core",
      concept: "netting",
      prompt:
        "Bank A owes Bank B $2,000,000 today. Bank B owes Bank A $1,800,000 today. After netting, what happens?",
      options: [
        "Both payments settle individually: $2,000,000 and $1,800,000 move",
        "Bank A transfers a net $200,000 to Bank B",
        "Bank B transfers a net $200,000 to Bank A",
        "Both obligations cancel and nothing moves",
      ],
      answer: 1,
      explanation:
        "[[netting]] offsets the obligations. Net position = $2,000,000 − $1,800,000 = $200,000 owed by Bank A to Bank B. Only $200,000 settles instead of $3,800,000 gross — a significant reduction in liquidity requirements. [[net-positions]] drive the settlement.",
    },
    {
      kind: "mc",
      id: "ch9-q5",
      difficulty: "core",
      concept: "settlement-delay",
      prompt:
        "A domestic wire transfer typically settles at T+0. A check deposit may take T+1 to T+5. What problem does the settlement window create for the customer?",
      options: [
        "The customer is charged interest during the window",
        "The customer cannot open new accounts until settlement completes",
        "Funds are not yet final — the bank holds the risk and uses holds to bridge the gap",
        "The sending bank must freeze the customer's entire balance",
      ],
      answer: 2,
      explanation:
        "The [[settlement-delay|settlement window]] creates a period where the payment has been initiated but interbank [[clearing-vs-settlement|settlement]] has not completed. The bank carries counterparty risk. [[holds]] bridge the gap for customers by reserving funds without posting final entries until settlement confirms.",
      explore: { label: "View settlement cycles", href: "/cycles" },
    },
    {
      kind: "multi",
      id: "ch9-q6",
      difficulty: "core",
      concept: "clearing-vs-settlement",
      prompt:
        "Which of the following occur during the clearing phase? (Select all that apply.)",
      options: [
        "Banks exchange payment instructions",
        "Central bank reserves move between banks",
        "Net positions are calculated",
        "The customer's account is debited at the sending bank",
        "Payment finality is achieved",
      ],
      answers: [0, 2],
      explanation:
        "[[clearing-vs-settlement|Clearing]] covers exchanging payment instructions and calculating [[netting|net positions]]. Reserve movement and payment finality happen at [[clearing-vs-settlement|settlement]], not during clearing. The customer's account is debited at initiation, before clearing even begins.",
      explore: { label: "View settlement cycles", href: "/cycles" },
    },
    {
      kind: "mc",
      id: "ch9-q7",
      difficulty: "core",
      concept: "net-positions",
      prompt:
        "What are nostro/vostro accounts used for in the context of interbank settlement?",
      options: [
        "Holding customer deposits at partner banks",
        "Tracking net positions and reconciling interbank obligations",
        "Earning interest on excess reserves at the central bank",
        "Storing failed payment instructions for retry",
      ],
      answer: 1,
      explanation:
        "Nostro/vostro accounts are mirror accounts that two banks maintain with each other. They allow each bank to track [[net-positions]] and reconcile exactly what one bank owes the other — essential for the post-clearing [[clearing-vs-settlement|settlement]] step.",
      explore: { label: "View settlements", href: "/settlements" },
    },
    {
      kind: "mc",
      id: "ch9-q8",
      difficulty: "core",
      concept: "settlement-delay",
      prompt:
        "Which payment type achieves settlement at T+0 (same day)?",
      options: [
        "Check",
        "ACH / direct debit",
        "Domestic wire (RTGS)",
        "International wire",
      ],
      answer: 2,
      explanation:
        "Domestic wire transfers using an RTGS (Real-Time Gross Settlement) system settle at T+0 — each payment is individually and immediately finalized. [[settlement-delay]] for ACH is T+1 to T+2, checks T+1 to T+5, and international wires T+1 to T+3.",
      explore: { label: "View settlement cycles", href: "/cycles" },
    },
    {
      kind: "truefalse",
      id: "ch9-q9",
      difficulty: "challenge",
      concept: "clearing-vs-settlement",
      prompt:
        "Settlement finality means the receiving bank has a legal, irrevocable claim to the funds.",
      answer: true,
      explanation:
        "[[clearing-vs-settlement|Settlement]] is the moment of finality — once reserves have moved at the central bank level, the transaction is irrevocable. This is fundamentally different from clearing, which is merely an agreement on amounts. Before settlement, counterparty risk remains.",
    },
    {
      kind: "mc",
      id: "ch9-q10",
      difficulty: "challenge",
      concept: "settlement-delay",
      prompt:
        "Why does the settlement window create counterparty risk for the receiving bank?",
      options: [
        "The receiving bank must pay interest until settlement",
        "The sending bank could fail or reverse the payment before settlement completes",
        "Central banks charge fees proportional to the settlement delay",
        "The customer can cancel any payment before it settles",
      ],
      answer: 1,
      explanation:
        "During the [[settlement-delay|settlement window]], the payment instruction has been sent but reserves have not yet moved. If the sending bank fails or the instruction is recalled (where permitted), the receiving bank may not receive the funds even though it has already credited its customer. This exposure is counterparty risk — eliminated only at [[clearing-vs-settlement|settlement]] finality.",
      explore: { label: "View settlements", href: "/settlements" },
    },
  ],
};
