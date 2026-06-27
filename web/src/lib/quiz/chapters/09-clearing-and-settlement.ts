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
      prompt: "Which payment type achieves settlement at T+0 (same day)?",
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
    {
      kind: "numeric",
      id: "ch9-q11",
      difficulty: "core",
      concept: "netting",
      prompt:
        "In a clearing cycle, Bank A pays Bank B $30,000 and Bank B pays Bank A $10,000. By how many dollars does Bank A's reserve account at the central bank decrease at settlement? (Enter a number of dollars.)",
      answer: 20000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[netting]] offsets the two obligations: Bank A owes $30,000 but is owed $10,000, so the net is $20,000 owed by Bank A to Bank B. Only **$20,000** of [[central-bank-reserves]] moves at settlement — not the $40,000 gross total. [[net-positions]] always sum to zero across all participants.",
    },
    {
      kind: "truefalse",
      id: "ch9-q12",
      difficulty: "intro",
      concept: "clearing-suspense",
      prompt:
        "When a bank initiates a payment, the customer's account is debited and an equal amount is credited to a clearing suspense account to hold the funds in transit.",
      answer: true,
      explanation:
        "At initiation the posting is: Debit the customer's deposit account (liability falls) and Credit [[clearing-suspense]] (liability rises). The suspense account holds in-transit funds on the network's behalf. At settlement the suspense is cleared — its balance returns to zero as reserves move and the payee is credited.",
    },
    {
      kind: "mc",
      id: "ch9-q13",
      difficulty: "intro",
      concept: "reserve-account",
      prompt:
        "In the multi-bank payment model, where does interbank settlement actually occur?",
      options: [
        "At each sending bank's internal general ledger",
        "At the central bank, via each bank's reserve account",
        "At a shared clearing house that holds funds independently of the central bank",
        "Directly between the two banks' nostro/vostro accounts, with no central bank involved",
      ],
      answer: 1,
      explanation:
        "Each commercial bank holds a [[reserve-account]] at the central bank. Settlement moves reserves from the paying bank's account to the receiving bank's account — the only way two institutions can transfer value without either writing in the other's books. The central bank's ledger is the single authoritative record.",
      explore: { label: "View central bank", href: "/central-bank" },
    },
    {
      kind: "mc",
      id: "ch9-q14",
      difficulty: "core",
      concept: "settlement-model-net",
      prompt:
        "Which correctly describes how net settlement differs from gross settlement?",
      options: [
        "Net settlement settles each payment instantly; gross settlement batches them end-of-day",
        "Net settlement accumulates payments across a cycle and settles only the net difference; gross settlement settles each payment individually and immediately",
        "Net settlement requires no central bank involvement; gross settlement uses the central bank",
        "Net settlement is only used for real-time payments; gross settlement is only for ACH",
      ],
      answer: 1,
      explanation:
        "In [[settlement-model-net|net settlement]], a clearing cycle accumulates all payments, computes net positions at cut-off, and only the net balance moves as central-bank reserves. In [[settlement-model-gross|gross settlement]], each payment triggers an immediate, individual reserve transfer with no netting. Net settlement dramatically reduces the total liquidity each bank needs on hand.",
    },
    {
      kind: "mc",
      id: "ch9-q15",
      difficulty: "challenge",
      concept: "settlement-model-gross",
      prompt:
        "UK Faster Payments delivers funds to customers in seconds, 24/7. Which settlement model does it use?",
      options: [
        "Gross (RTGS) settlement — because the customer experience is instant",
        "Deferred net settlement — despite the instant customer experience, payments batch and net before reserves move",
        "Real-time gross settlement identical to Fedwire",
        "Clearing without settlement — no reserves move at all",
      ],
      answer: 1,
      explanation:
        "Customer-perceived speed and the underlying [[settlement-model-gross|settlement model]] are independent. UK Faster Payments feels instant but actually settles on a deferred [[settlement-model-net|net]] basis — it is not a gross-settlement example. True [[settlement-model-gross|gross settlement]] means each payment *individually* moves central-bank reserves, as with Fedwire (US) or CHAPS (UK) for high-value wires.",
    },
    {
      kind: "multi",
      id: "ch9-q16",
      difficulty: "core",
      concept: "clearing-suspense",
      prompt:
        "Which of the following correctly describe the clearing suspense account that each bank maintains? (Select all that apply.)",
      options: [
        "It is a liability account on the bank's books",
        "It holds in-transit funds after the customer is debited, until settlement moves reserves",
        "Its balance returns to zero after each settlement cycle completes",
        "It holds the bank's reserve balances at the central bank",
      ],
      answers: [0, 1, 2],
      explanation:
        "The [[clearing-suspense]] account is a liability (the bank holds funds on the network's behalf) that accumulates in-transit amounts during the clearing window. At settlement, the suspense is unwound: the bank's reserve-at-central-bank asset adjusts, and the suspense balance returns to zero. Option D describes a [[reserve-account]], not a suspense account.",
      explore: { label: "View settlements", href: "/settlements" },
    },
    {
      kind: "truefalse",
      id: "ch9-q17",
      difficulty: "core",
      concept: "settlement-model-net",
      prompt:
        "Both SEPA Credit Transfer and SEPA Direct Debit use the net settlement model in this system.",
      answer: true,
      explanation:
        "Both schemes implement [[settlement-model-net|net settlement]]: payments accumulate during a clearing cycle, net positions are computed at cut-off, and only the net balances settle through reserve movements at the central bank. The direction of initiation differs (push vs pull, mandate required for direct debit), but both schemes settle on the same net basis.",
      explore: { label: "View schemes", href: "/schemes" },
    },
    {
      kind: "numeric",
      id: "ch9-q18",
      difficulty: "challenge",
      concept: "net-positions",
      prompt:
        "In a clearing cycle: Bank A pays Bank B $45,000; Bank B pays Bank A $15,000; Bank B pays Bank C $10,000; Bank C pays Bank A $5,000. What is Bank B's net position in dollars? (Positive means Bank B receives net; enter a positive number if Bank B is a net receiver.)",
      answer: 20000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Bank B's [[net-positions|net position]] = inflows − outflows = $45,000 − ($15,000 + $10,000) = **+$20,000**. Bank B is a net receiver: its reserve account at the central bank rises by $20,000 at settlement. As a sanity check: Bank A's net is −$25,000, Bank C's is +$5,000; all three sum to zero — the defining property of net positions.",
    },
    {
      kind: "multi",
      id: "ch9-q19",
      difficulty: "challenge",
      concept: "payment-lifecycle",
      prompt:
        "Which of the following events happen at the settlement step — not at initiation or clearing? (Select all that apply.)",
      options: [
        "The payer's deposit account is debited",
        "Central-bank reserves move from the paying bank to the receiving bank",
        "Net positions across participants are computed",
        "The payee's deposit account is credited (creditor leg posted)",
        "The payment becomes final and irrevocable",
      ],
      answers: [1, 3, 4],
      explanation:
        "At the [[payment-lifecycle|settlement]] step: the central bank moves reserves, the creditor leg delivers funds to the payee, and finality is achieved — the payment can no longer be unwound. The payer's debit (option A) is the initiation step; net-position computation (option C) is the clearing step.",
      explore: { label: "View settlements", href: "/settlements" },
    },
    {
      kind: "numeric",
      id: "ch9-q20",
      difficulty: "core",
      concept: "central-bank-reserves",
      prompt:
        "Bank A initiates $80,000 in outbound payments and receives $30,000 in inbound payments during a clearing cycle. After netting, how many dollars of central-bank reserves does Bank A transfer at settlement? (Enter a number of dollars.)",
      answer: 50000,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Bank A's net outflow = $80,000 − $30,000 = **$50,000**. Under [[settlement-model-net|net settlement]], only this net amount moves as [[central-bank-reserves]] — not the $110,000 gross total. This is why netting dramatically reduces the liquidity each participant must hold to cover a full cycle.",
    },
  ],
};
