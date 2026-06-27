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
      prompt:
        "Which statement correctly distinguishes clearing from settlement in the interbank context?",
      options: [
        "Clearing is the exchange and netting of payment instructions; settlement is the movement of reserves between banks at the central bank",
        "Settlement is the exchange and netting of payment instructions; clearing is the movement of reserves at the central bank",
        "Clearing and settlement are two names for the same process — both involve moving reserves",
        "Clearing moves reserves; settlement only confirms that the instructions were exchanged",
      ],
      answer: 0,
      explanation:
        "[[clearing-vs-settlement]]: clearing is pure bookkeeping — the banks agree on net positions and no central-bank money moves. Settlement is the act: reserves transfer between banks' accounts at the central bank, and that transfer is final and irrevocable.",
    },
    {
      kind: "mc",
      id: "ch10-q2",
      difficulty: "intro",
      concept: "reserve-account",
      prompt: "What is a bank's 'Reserve at Central Bank' account used for?",
      options: [
        "Recording the bank's overnight interbank lending revenue",
        "Holding the bank's claim on the central bank, used as the settlement vehicle for interbank payments",
        "Tracking outstanding customer cheques that have not yet cleared",
        "Calculating the bank's capital adequacy ratio",
      ],
      answer: 1,
      explanation:
        "The [[reserve-account]] — 'Reserve at Central Bank' — is an Asset on the bank's own books representing its claim on the central bank. It is the account that moves (decreases for the payer, increases for the receiver) when interbank settlement actually occurs.",
      explore: { label: "View central bank reserves", href: "/central-bank" },
    },
    {
      kind: "mc",
      id: "ch10-q3",
      difficulty: "intro",
      concept: "account-type-asset",
      prompt:
        "A bank's 'Reserve at Central Bank' appears on its balance sheet as which type of account?",
      options: [
        "Liability — because the central bank controls the reserves",
        "Equity — because it represents the bank's ownership interest in the central bank",
        "Asset — because it is the bank's claim on the central bank",
        "Revenue — because reserves earn interest from the central bank",
      ],
      answer: 2,
      explanation:
        "'Reserve at Central Bank' is an [[account-type-asset]]: it represents something of value the bank owns — its claim on the central bank. Just as a customer's bank deposit is the bank's liability, a bank's deposit at the central bank is the bank's asset.",
    },
    {
      kind: "truefalse",
      id: "ch10-q4",
      difficulty: "intro",
      concept: "account-type-asset",
      prompt:
        "When Bank A deposits reserves at the central bank, it is Bank A — not the central bank — that records those reserves as an asset on its own books.",
      answer: true,
      explanation:
        "The deposit creates a mirror pair: Bank A records 'Reserve at Central Bank' as an [[account-type-asset]] — its claim on the central bank. The central bank simultaneously records a liability ('Reserve: Bank A') — what it owes back to Bank A. Both ledgers capture the same pile of reserves, but as opposite account types.",
      explore: { label: "View central bank reserves", href: "/central-bank" },
    },
    {
      kind: "mc",
      id: "ch10-q5",
      difficulty: "intro",
      concept: "central-bank-reserves",
      prompt:
        "In the central bank's own ledger, how are the reserves belonging to Bank A recorded?",
      options: [
        "As an asset — 'Reserve at Bank A' on the asset side",
        "As equity — representing Bank A's ownership stake in the central bank",
        "As a liability — 'Reserve: Bank A', because the central bank owes those reserves to Bank A",
        "As revenue — because the central bank earns seigniorage on the reserves",
      ],
      answer: 2,
      explanation:
        "[[central-bank-reserves]] appear as a **liability** in the central bank's ledger. The central bank holds those reserves on behalf of Bank A and owes them back — exactly the same relationship as a customer deposit at a commercial bank. This is the vostro side of the nostro/vostro mirror.",
    },
    {
      kind: "mc",
      id: "ch10-q6",
      difficulty: "core",
      concept: "clearing-suspense",
      prompt:
        "Alice (Bank A) initiates a $300 payment to Bob (Bank B). Alice's deposit account is debited $300. Which account at Bank A is credited?",
      options: [
        "Reserve at Central Bank — because the money is on its way to the central bank",
        "Clearing Suspense — because the funds are in transit and have not yet settled",
        "Bob's deposit account at Bank B — because that is the ultimate destination",
        "Bank A's equity account — because the liability to Alice has decreased",
      ],
      answer: 1,
      explanation:
        "At initiation, Bank A debits Alice's deposit and credits its [[clearing-suspense]] account — also a liability — which captures the in-transit value. No central-bank reserves move at this stage; that happens only at settlement.",
    },
    {
      kind: "mc",
      id: "ch10-q7",
      difficulty: "core",
      concept: "reserve-account",
      prompt:
        "At settlement, Bank A is a net payer of $400. What entry does Bank A post to its 'Reserve at Central Bank' account?",
      options: [
        "Debit $400 — the reserve asset increases",
        "Credit $400 — the reserve asset decreases",
        "No entry — the central bank handles all reserve adjustments unilaterally",
        "Credit $400 to reserve and simultaneously credit clearing suspense $400",
      ],
      answer: 1,
      explanation:
        "[[reserve-account]] has an asset normal balance: debits increase it, credits decrease it. When Bank A pays out reserves at settlement, its reserve asset falls — recorded as a credit of $400. Simultaneously, clearing suspense (a liability) is debited to close the in-transit funds, restoring it to zero.",
    },
    {
      kind: "mc",
      id: "ch10-q8",
      difficulty: "core",
      concept: "net-positions",
      prompt:
        "In a clearing cycle, Bank A has $900 in outgoing payment instructions and $350 in incoming instructions. What is Bank A's net position?",
      options: [
        "+$550 — Bank A is a net receiver of reserves",
        "−$550 — Bank A is a net payer of reserves",
        "−$900 — the full gross outgoing amount",
        "$0 — net positions always cancel within a single bank",
      ],
      answer: 1,
      explanation:
        "[[net-positions]] = incoming − outgoing = $350 − $900 = −$550. A negative net position means Bank A must pay $550 of central-bank reserves at settlement, not the full $900 gross.",
    },
    {
      kind: "mc",
      id: "ch10-q9",
      difficulty: "core",
      concept: "central-bank-reserves",
      prompt:
        "Bank B receives a net inflow of $550 in a clearing cycle. What happens to the 'Reserve: Bank B' liability in the central bank's ledger at settlement?",
      options: [
        "It is debited $550 — reducing the central bank's obligation to Bank B",
        "It is credited $550 — increasing the central bank's obligation to Bank B",
        "It is unchanged — the central bank does not track individual bank positions",
        "It is debited $550 in the central bank's asset account, not its liability",
      ],
      answer: 1,
      explanation:
        "In the central bank's ledger, 'Reserve: Bank B' is a [[central-bank-reserves|liability]] — what the central bank owes Bank B. When Bank B receives reserves, that obligation increases, recorded as a credit. This mirrors Bank B's own 'Reserve at Central Bank' asset rising by the same amount.",
    },
    {
      kind: "truefalse",
      id: "ch10-q10",
      difficulty: "core",
      concept: "central-bank-reserves",
      prompt:
        "Bank A's 'Reserve at Central Bank' (an asset on Bank A's books) and the central bank's 'Reserve: Bank A' (a liability on the central bank's books) must always show the same dollar balance.",
      answer: true,
      explanation:
        "These two entries are mirror images of the same pile of reserves — one recorded as Bank A's claim, the other as the central bank's obligation. [[central-bank-reserves]]: if they ever diverged, one party would have a record of money the other side doesn't — precisely the error that double-entry reconciliation exists to make impossible.",
    },
    {
      kind: "truefalse",
      id: "ch10-q11",
      difficulty: "core",
      concept: "double-entry",
      prompt:
        "During the clearing phase, no central-bank reserves move, because clearing is purely the exchange and netting of instructions. In-transit funds cannot vanish mid-flight thanks to the rules of double-entry bookkeeping.",
      answer: true,
      explanation:
        "[[double-entry]] requires every credit to have an equal debit. When Alice's deposit is debited at initiation, Bank A's clearing suspense is credited — the funds rest there rather than disappearing. The clearing phase computes net positions; no reserve entry is posted until settlement closes the suspense and transfers reserves.",
    },
    {
      kind: "multi",
      id: "ch10-q12",
      difficulty: "core",
      concept: "clearing-suspense",
      prompt:
        "Alice (Bank A) pays Bob (Bank B) $200. Which of the following statements about Bank A's clearing suspense account are correct? (Select all that apply.)",
      options: [
        "Clearing suspense is a liability on Bank A's balance sheet",
        "Clearing suspense is credited when Alice's payment is initiated",
        "Clearing suspense returns to zero after the settlement cycle completes",
        "Clearing suspense is the same account as Bank A's Reserve at Central Bank",
        "A non-zero clearing suspense balance after settlement signals funds still in transit",
      ],
      answers: [0, 1, 2, 4],
      explanation:
        "[[clearing-suspense]] is a temporary liability: credited at initiation (Step 1) and debited at settlement (Step 3), leaving a net balance of zero for each settled payment. It is a completely separate account from the reserve asset — one captures in-transit obligations; the other is the bank's funded claim on the central bank.",
    },
    {
      kind: "numeric",
      id: "ch10-q13",
      difficulty: "core",
      concept: "net-positions",
      prompt:
        "Bank A owes Bank B $500 and Bank B owes Bank A $300 in the same clearing cycle. How many dollars of central-bank reserves actually move at settlement?",
      answer: 200,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[net-positions]] offset each other: Bank B's $300 obligation cancels $300 of Bank A's $500, leaving a **net of $200** flowing from Bank A to Bank B. Only this net amount clears through the central bank — not the $800 gross total.",
    },
    {
      kind: "numeric",
      id: "ch10-q14",
      difficulty: "core",
      concept: "reserve-account",
      prompt:
        "Bank A starts the day with a reserve balance of $5,000. In the clearing cycle its net position is −$1,200 (Bank A is a net payer). What is Bank A's reserve balance after settlement?",
      answer: 3800,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "A negative net position means Bank A pays that amount of central-bank reserves. Its [[reserve-account]] balance falls: $5,000 − $1,200 = **$3,800**.",
    },
    {
      kind: "mc",
      id: "ch10-q15",
      difficulty: "core",
      concept: "clearing-suspense",
      prompt:
        "After a successful settlement cycle at Bank A, what should the balance in Bank A's clearing suspense account be?",
      options: [
        "Equal to the sum of all outgoing payments initiated in the cycle",
        "Equal to Bank A's net position in the cycle",
        "Zero — all in-transit funds have been settled",
        "Negative — because clearing suspense is debited at settlement",
      ],
      answer: 2,
      explanation:
        "[[clearing-suspense]] is credited when payments are initiated and debited when they settle. Because settlement closes every in-transit item, the net balance returns to **zero** after a complete cycle. Any residual balance signals payments that have not yet settled.",
    },
    {
      kind: "mc",
      id: "ch10-q16",
      difficulty: "challenge",
      concept: "netting",
      prompt:
        "Bank A and Bank B exchange 1,000 individual payment instructions in a cycle — 500 from A to B and 500 from B to A — netting to a single $200 transfer from A to B. What is the primary benefit of this netting?",
      options: [
        "It reduces regulatory capital requirements for both banks",
        "It eliminates the need for any reserve balances at the central bank",
        "It drastically reduces the amount of central-bank liquidity needed to settle the full day's payments",
        "It allows banks to settle payments without a trusted central counterparty",
      ],
      answer: 2,
      explanation:
        "[[netting]] compresses thousands of gross payment flows into a single net figure. Instead of requiring reserves equal to every individual gross flow, banks settle only the residual net. This is the core efficiency gain of net settlement: banks can support far larger payment volumes with much smaller reserve buffers.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "multi",
      id: "ch10-q17",
      difficulty: "challenge",
      prompt:
        "Three banks settle a clearing cycle. Their net positions are: Bank A = −$300, Bank B = +$500, Bank C = −$200. Select ALL true statements.",
      options: [
        "The three net positions sum to zero",
        "Bank B receives reserves at settlement",
        "Banks A and C both pay reserves at settlement",
        "The total reserves transferred at the central bank equal $1,000 (the sum of all absolute net positions)",
        "Bank A's reserve balance falls by $300 after settlement",
      ],
      answers: [0, 1, 2, 4],
      explanation:
        "Net positions always sum to zero across all participants (−300 + 500 − 200 = 0) — reserves are conserved, not created. Bank B is a net receiver; A and C are net payers. The total reserve flow is $500 (not $1,000): $300 from A and $200 from C both flow to B, totalling $500 received — matching B's net position.",
    },
    {
      kind: "truefalse",
      id: "ch10-q18",
      difficulty: "challenge",
      concept: "settlement-delay",
      prompt:
        "A net-settled payment scheme achieves settlement finality at the moment the payer's bank debits the customer's account.",
      answer: false,
      explanation:
        "The customer's account is debited at initiation — the funds move into clearing suspense. [[settlement-delay|Settlement finality]] does not occur until the central-bank reserve transfer completes at the end of the clearing cycle. Until reserves actually move, the payment is clearing but not final, and the funds remain in suspense.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "numeric",
      id: "ch10-q19",
      difficulty: "challenge",
      concept: "net-positions",
      prompt:
        "In a three-bank clearing cycle: Bank A sends $600 to Bank B; Bank B sends $400 to Bank C; Bank C sends $250 to Bank A. By how many dollars do Bank B's central-bank reserves increase at settlement? (Enter a positive number — Bank B is a net receiver.)",
      answer: 200,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Bank B's [[net-positions|net position]] = received from A ($600) − paid to C ($400) = **+$200**. Net positions sum to zero across all three banks (A: −$350, B: +$200, C: +$150 → total 0), confirming no reserves are created or destroyed — only redistributed.",
    },
    {
      kind: "multi",
      id: "ch10-q20",
      difficulty: "challenge",
      prompt:
        "Alice (Bank A) pays Bob (Bank B) $150. This is the only payment in the cycle (Bank A net = −$150, Bank B net = +$150). Select ALL correct settlement entries across all three ledgers.",
      options: [
        "Central Bank: Debit Reserve: Bank A $150, Credit Reserve: Bank B $150",
        "Bank A: Debit Clearing Suspense $150, Credit Reserve at Central Bank $150",
        "Bank A: Debit Alice's deposit $150 (at settlement)",
        "Bank B: Debit Reserve at Central Bank $150",
        "Bank B: Credit Bob's deposit account $150",
      ],
      answers: [0, 1, 3, 4],
      explanation:
        "At settlement three ledgers move in lockstep: (1) the **central bank** debits Bank A's reserve liability and credits Bank B's; (2) **Bank A** closes its [[clearing-suspense]] with a debit and records the outgoing reserves with a credit to its reserve asset; (3) **Bank B** debits (raises) its reserve asset and credits Bob's deposit. Alice's deposit was debited at *initiation* — not at settlement.",
      explore: { label: "View central bank reserves", href: "/central-bank" },
    },
  ],
};
