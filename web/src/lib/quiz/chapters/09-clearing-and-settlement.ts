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
      explore: { label: "View settlement cycles", href: "/clearing-house/cycles" },
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
        "[[clearing-vs-settlement|Clearing]] covers exchanging payment instructions and calculating [[netting|net positions]]. Reserve movement and payment finality happen at [[clearing-vs-settlement|settlement]], not during clearing. The customer's account is debited by the payer's own bank before clearing begins either way — at initiation for a credit transfer, and when the collection arrives for a direct debit, which the *payee's* bank submits.",
      explore: { label: "View settlement cycles", href: "/clearing-house/cycles" },
    },
    {
      kind: "mc",
      id: "ch9-q7",
      difficulty: "core",
      concept: "settlement-finality",
      prompt:
        "A single settled payment is sent back as a return. The central bank reverses the reserves between the two banks and states both accounts. What do the two member banks then have to do, and what is their position until they do?",
      options: [
        "Nothing — a return is one payment, so the settlement agent posts every leg of it in one transaction",
        "Each books its own reserve mirror locally, and the one bank that still owes a customer leg posts it from the relayed return message; until each has booked it carries an unreconciled position exactly as after a cut-off",
        "They confirm the reversal back to the central bank, which is only final once both have acknowledged it",
        "They wait for the next clearing cycle, where the return is netted with everything else",
      ],
      answer: 1,
      explanation:
        "A return is a cut-off's shape, one payment wide. [[settlement-finality|The reserve reversal is final]] the moment the central bank commits it, and nothing either member does next unwinds it — including failing to book. Each bank is then *told*, by a `camt.053`, and books its own reserve mirror in a [[unit-of-work|unit of work]] of its own, because no institution may write in another's book. Only **one** customer leg is still outstanding at that point: the returning bank posted its own *before* the `pacs.004` existed — that ordering is what let it [[allows-return|refuse]] — so the leg that is left belongs to the other bank, which posts it from the `pacs.004` the clearing house had held back until the return was final. Until a bank has booked, its [[clearing-suspense|clearing suspense]] has not returned to zero and there is no settlement-advice row against the reference — the [[unreconciled-position|unreconciled position]], reached by a second route. The answer about **waiting for the next clearing cycle** describes the real SEPA R-cycle rather than this model, which settles a return immediately.",
      explore: { label: "View settlements", href: "/clearing-house/settlements" },
    },
    {
      kind: "mc",
      id: "ch9-q8",
      difficulty: "core",
      concept: "bank-admission",
      prompt:
        "A bank is given a place in the scheme. Two things have to come into existence: its settlement account at the central bank, and its entry in the clearing house's routing directory. In which order, and why?",
      options: [
        "The routing entry first — the scheme has to be able to reach the bank before anyone can open an account for it",
        "The settlement account first; the clearing house then writes its routing entry from what the account servicer opened, because a scheme will not route to a member that cannot settle",
        "Both at once, in the single transaction that admits the bank, so it can never exist without the accounts it needs",
        "The bank opens both itself — one in each institution's book — and tells the two institutions afterwards",
      ],
      answer: 1,
      explanation:
        "[[bank-admission|Admission is a sequence]], and the order carries the meaning. The central bank opens the [[settlement-account|settlement account]] in its own book **first** — and allocates the applicant's [[bank-code|bank code]] in the same unit of work, out of a second register of its own. The clearing house then writes its [[routing-roster|routing entry]] from what the agent opened, the allocation included, so a bank the scheme routes to is one it can already settle for. Scheme membership follows the settlement account, not the other way round: routing to a bank the settlement agent will not hold an account for would produce net positions nobody could discharge. The answer about **one transaction admitting the bank** would be one write covering three institutions' records, so that \"a bank can never exist without the accounts it needs\". No real admission has that guarantee: a bank is [[bank-founding|licensed and built]] before any scheme has heard of it. The answer that has **the bank open both accounts itself** is the one thing none of them may do — no institution writes in another's book, which is exactly why this is four units of work and not one.",
      explore: { label: "View central bank", href: "/central-bank" },
    },
    {
      kind: "mc",
      id: "ch9-q9",
      difficulty: "core",
      concept: "book-transfer",
      prompt:
        "Alice and Aaron both hold accounts at Aurora Bank. Alice pays Aaron €100. What does the clearing house do with it?",
      options: [
        "Nets it with Aurora's other payments for the cycle, leaving Aurora's net position €100 different",
        "Nothing — it never hears about it. Aurora posts both legs in its own book, and no obligation between institutions comes into existence",
        "Clears and settles it as usual; the two reserve movements happen to cancel, so Aurora's reserve ends up unchanged",
        "Refuses it, and there is nothing else Alice can do — a payment needs two banks",
      ],
      answer: 1,
      explanation:
        "A [[book-transfer]] is the one payment with a single institution at both ends. Aurora debits Alice's deposit and credits Aaron's in one posting: no position for the clearing house to [[netting|net]], no reserves for the settlement agent to move, no [[clearing-suspense|suspense]] — suspense holds money that has left one bank and not yet reached another, and this money never left — and no statement that could tell Aurora about a book it already holds.\n\nThe answer about **reserve movements that cancel** is what actually happens when such a payment is submitted to a clearing house anyway, and it is worse than it sounds: measured, a cut-off whose only payment netted to zero settled nothing and stranded, and one bank's own record of its reserve moved by an amount the central bank's record of it did not. That is why it is refused at submission.\n\nThe last answer gets the refusal right and the consequence wrong. Refusing the *clearing* route is a statement about the route, not about the payment: the transfer is an ordinary product and Alice's bank performs it. What she has to change is which of the two she asks for.",
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
      explore: { label: "View settlements", href: "/clearing-house/settlements" },
    },
    {
      kind: "numeric",
      id: "ch9-q11",
      difficulty: "core",
      concept: "net-positions",
      prompt:
        "Three banks settle together. Bank A's net position is −$40,000 and Bank B's net position is +$25,000. Using the zero-sum property of net settlement, what is Bank C's net position in dollars? (Positive means Bank C is a net receiver; enter a positive number if Bank C receives net.)",
      answer: 15000,
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "[[net-positions]] across all participants in a clearing cycle always sum to zero — every dollar sent by a net payer is received by a net receiver. Bank A (−$40,000) + Bank B (+$25,000) + Bank C = $0, so Bank C = **+$15,000**. Bank C's [[reserve-account]] at the central bank rises by $15,000 at settlement.",
    },
    {
      kind: "truefalse",
      id: "ch9-q12",
      difficulty: "intro",
      concept: "clearing-suspense",
      prompt:
        "When a bank submits a credit transfer for its own customer, that customer's account is debited and an equal amount is credited to a clearing suspense account to hold the funds in transit.",
      answer: true,
      explanation:
        "The posting is: Debit the customer's deposit account (liability falls) and Credit [[clearing-suspense]] (liability rises). The suspense account holds in-transit funds on the network's behalf. At settlement the suspense is cleared — its balance returns to zero as reserves move and the payee is credited. The prompt says *credit transfer* deliberately: the payer's own bank always posts this leg, but on a direct debit that bank is the one **receiving** the collection, so it posts when the instruction reaches it rather than when the payment was submitted.",
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
        "The [[clearing-suspense]] account is a liability (the bank holds funds on the network's behalf) that accumulates in-transit amounts during the clearing window. The cut-off unwinds it — but the bank does that itself, on the advices it is sent, *after* the central bank has already [[settlement-finality|settled]]: its reserve-at-central-bank asset adjusts on the `camt.053`, its creditor legs clear on the `pacs.002` fan-out, and only once both are booked does the balance return to zero. The option about **holding reserve balances at the central bank** describes a [[reserve-account]], not a suspense account.",
      explore: { label: "View settlements", href: "/clearing-house/settlements" },
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
      explore: { label: "View schemes", href: "/clearing-house/schemes" },
    },
    {
      kind: "mc",
      id: "ch9-q18",
      difficulty: "challenge",
      concept: "business-day",
      prompt:
        "A clearing day runs three phases in this order: the clearing house closes and nets every open cycle, the settlement agent discharges the net positions, and the clearing house releases each receiving bank's file. What breaks if the first and third are swapped?",
      options: [
        "Nothing about the money — only the order the operator's console shows events in",
        "Receiving banks would credit customers against a cut-off that might still be refused, which is settlement risk invented by an ordering",
        "The net positions would be computed from the wrong set of payments, so the arithmetic would no longer sum to zero",
        "The payer's bank would never learn that its own payments settled",
      ],
      explanation:
        "[[business-day|Settle, then release]] is the ordering the whole day is arranged around. A file released before its cycle settles hands a receiving bank instructions whose funds are not yet final — and a net payer short of reserves is refused *after* several banks have already credited customers. Reversed, the failure is not recoverable by a message: the money is in customers' accounts.\n\nWhat the correct order costs is the receiving bank's ability to say **no**. By the time it looks at a payment the money is in its own [[clearing-suspense|clearing suspense]], so an address it does not hold or a payer it cannot collect from is a [[allows-return|return]] rather than a rejection — which is exactly what those codes are in SEPA, and for exactly this reason.",
      answer: 1,
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
        "At the [[payment-lifecycle|settlement]] step: the central bank moves reserves, and that is where [[settlement-finality|finality]] is achieved — the payment can no longer be unwound. The [[creditor-leg]] delivers funds to the payee, and it is posted by the **payee's own bank**, in its own unit of work, when the clearing house [[business-day|releases]] the instruction to it — which happens only once the cycle is final, and is the first time that bank sees the payment at all. The payer's debit (option A) is the initiation step; net-position computation (option C) is the clearing step.\n\n**Notice how many different institutions' books that list touches, and that no two of them are in the same database.** A payment is [[store-split|three rows]] — the payer's bank's, the payee's bank's and the clearing house's — and the events above are each written by whichever institution performed them, on its own copy. That is why the payer's debit and the payee's credit cannot be one transaction, and why the interval between the reserves moving and the payee being credited is a real [[unreconciled-position|unreconciled position]] rather than something a database could hide.",
      explore: { label: "View settlements", href: "/clearing-house/settlements" },
    },
    {
      kind: "truefalse",
      id: "ch9-q20",
      difficulty: "core",
      concept: "target-calendar",
      prompt:
        "A payment submitted on a Friday afternoon clears and settles over the weekend, because the settlement agent's ledger is a database and databases do not observe holidays.",
      answer: false,
      explanation:
        "Clearing and settlement run on the settlement agent's calendar — in the euro area [[target-calendar|TARGET]], which is shut at weekends and on six named days. A Friday-afternoon payment waits in its bank's [[payment-hub|hub]] and clears on Monday.\n\nThe weekend still **happens**, though, and that is the half worth remembering: the date advances and every bank still runs its end of day, because [[interest-accrual|interest accrues]] over a weekend — which is the entire reason [[day-count|day-count conventions]] exist. A system that skipped from Friday to Monday would have no Saturday to accrue on.\n\nNote whose calendar it is. German banks are shut on 3 October and TARGET is not, so a payment submitted that morning clears and settles that afternoon: a national holiday closes branches, not the settlement agent.",
    },
    {
      kind: "mc",
      id: "ch9-q21",
      difficulty: "core",
      concept: "settlement-finality",
      prompt:
        "The central bank commits its netting transaction for a cycle. A member bank is then told, and its own posting of the mirror leg fails. What is the state of the settlement?",
      options: [
        "The settlement is unwound, because one member could not book it",
        "The settlement is final; that member has an unreconciled position to resolve",
        "The settlement is provisional until every member confirms",
        "The cycle reverts to Closed and the clearing house re-presents it",
      ],
      answer: 1,
      explanation:
        "[[settlement-finality|Settlement is final at the central bank]] and the participants catch up afterwards. Once that one unit of work commits, the reserves have moved and nothing a member does next unwinds it. In the EU this is not a modelling convenience but the **Settlement Finality Directive** (98/26/EC), which exists so that one participant's failure cannot reach back into a batch that has already settled. What the failed posting leaves is an [[unreconciled-position|unreconciled position]]: a [[clearing-suspense|clearing suspense]] that has not returned to zero, with **no** settlement-advice row against the cycle — the row and the mirror leg are one unit of work, so a failed posting leaves nothing behind rather than a half-written record. A refusal *before* the commit is the other outcome, and it is equally decisive: `RJCT`/`AM04`, nothing posted anywhere, every payment still Cleared.",
      explore: { label: "View settlements", href: "/clearing-house/settlements" },
    },
    {
      kind: "multi",
      id: "ch9-q22",
      difficulty: "challenge",
      concept: "nostro-reconciliation",
      prompt:
        "After a cut-off, a bank's clearing suspense should return to zero. Which statements about how it gets there are correct? (Select all that apply.)",
      options: [
        "The central bank's statement says what the bank's reserve moved by",
        "The clearing house's per-payment advices say which payments settled",
        "Both advices come from the central bank, which is the only party that knows",
        "The suspense returns to zero only if the two advices agree",
        "The bank computes the movement itself by reading the cycle's net positions",
      ],
      answers: [0, 1, 3],
      explanation:
        "A bank reconciles [[nostro-reconciliation|two advices from two institutions against one balance]]. The central bank sends a `camt.053` stating what that bank's [[reserve-account|reserve account]] did — the mirror leg. The clearing house sends one `pacs.002`/`ACSC` per payment — the [[creditor-leg|creditor legs]]. The [[clearing-suspense]] account returns to zero only if the two agree, which is precisely why the split matters: two **senders** make a check possible, and if both advices had one sender the bank would be checking it against itself. The option about the bank **reading the cycle's net positions for itself** is what the system deliberately does *not* do — a member never reads the cycle row; the cycle belongs to the clearing house.",
    },
  ],
};
