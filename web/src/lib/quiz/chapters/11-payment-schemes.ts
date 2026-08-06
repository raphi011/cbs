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
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
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
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
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
      explore: { label: "See settlement cycles", href: "/clearing-house/cycles" },
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
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
    },
    {
      kind: "numeric",
      id: "ch11-q7",
      difficulty: "core",
      concept: "netting",
      prompt:
        "In one clearing cycle, Bank A submits 4 outgoing payments of $75 each to Bank B, and Bank B submits 3 outgoing payments of $40 each to Bank A. How many dollars of central-bank reserves move at settlement?",
      answer: 180,
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "[[netting]] aggregates all payments before computing net positions. Bank A's gross outflow = 4 × $75 = $300; Bank A's gross inflow = 3 × $40 = $120; net[A] = −$300 + $120 = −$180. Bank B's net = +$180. Only **$180** of reserves transfers — netting collapses the $420 gross total into a single $180 net flow.",
      explore: { label: "See settlement cycles", href: "/clearing-house/cycles" },
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
        "Which lifecycle state is a payment in immediately after the debtor leg is posted?",
      options: ["Initiated", "Accepted", "Cleared", "Settled"],
      answer: 0,
      explanation:
        "**Initiated.** The [[debtor-leg]] is posted by the payer's own bank, and that bank does not get to say the payment was accepted — only the clearing house does, by taking it into the open cycle for its scheme. So the payer's money can be sitting in [[clearing-suspense]] while the [[payment-lifecycle]] still reads *Initiated*. On a push the bank posts it when it submits; on a pull it posts it when the collection reaches it. Either way, **Accepted** comes later and from somebody else.",
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
        "The scheme permits a settled payment to be sent back by an R-transaction",
        "Money flows from the creditor's account to the debtor's account",
        "Each payment settles individually without netting",
      ],
      answers: [0, 1],
      explanation:
        "SDD is [[scheme-direction-pull]] — the payee's bank initiates, and its submission posts nothing. It [[requires-mandate]] (a signed authorization), which SCT does not: a payer instructing their own bank *is* the authorization. The return option is the trap — [[allows-return]] reports **true for both schemes** here (`payment/scheme.go` sets it on `SCT` and `SDD` alike, and `PostReturnLegTx` refuses only a scheme that reports false), and that matches SEPA: a credit transfer return is a real R-transaction, sent by the beneficiary's bank when it cannot apply the funds. What SDD alone gives the *debtor* is the dispute — the 8-week refund — and this model puts no window on it. Both schemes use net settlement, not individual (gross) settlement.",
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
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
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "[[settlement-model-net]] computes the net position: $10,000 outgoing − $9,500 incoming = **$500 net outflow**. Bank A's reserve account at the central bank decreases by only $500, not by the $10,000 gross outgoing total.",
      explore: { label: "See settlement cycles", href: "/clearing-house/cycles" },
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
        "The [[payment-lifecycle]] state for a failed payment before clearing is **Rejected**. The debtor-leg posting is reversed — a compensating entry credits the payer's account back and clears Clearing Suspense to zero. Note that this is **two acts by two institutions**: the clearing house marks the payment Rejected, and the payer's own bank reverses the leg when the rejection reaches it, because nobody else may post in that bank's suspense. The **Returned** state is distinct: it applies *after* a payment has already settled.",
    },
    {
      kind: "mc",
      id: "ch11-q16",
      difficulty: "core",
      concept: "bank-founding",
      prompt:
        "A new bank is created and the API answers 202 with its status reading Founded — the scheme has not answered its application yet. What can that bank do in the meantime?",
      options: [
        "Nothing. Founded is a half-written record, and the bank only becomes usable once the scheme answers",
        "Open customer accounts, publish products and add ledgers — but not fund a customer account, because funding raises a reserve at the central bank and no settlement agent holds one for it yet",
        "Everything a member can do. Founded only records that an operator has not ticked the bank off yet",
        "Take cash deposits, but not open the accounts that would hold them",
      ],
      answer: 1,
      explanation:
        "**A bank exists before it joins a scheme.** [[bank-founding|Founding]] gives it a book, a chart of accounts, its [[participant-assets|per-asset plumbing accounts]] and a deposit product, and its own book is then unrestricted. What it lacks is everything belonging to somebody else: no central bank holds a [[settlement-account|settlement account]] for it and no clearing house holds a [[routing-roster|routing entry]], so nothing it takes part in can settle. Funding is the trap, and the API refuses it with a `422` naming the *membership* rather than the account: cash paid in raises the bank's reserve at the central bank in the same [[unit-of-work|unit of work]], and there is no reserve to raise. Option A is the ruling this system reversed — admission used to be one transaction writing all three institutions' records at once, \"so a bank can never exist without the accounts it needs\", and no real admission has that guarantee. Both states are ordinary: [[bank-admission|admission is a conversation]], so a bank read straight back may still be `Founded` and perfectly healthy.",
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
        "Whether settled payments may be returned",
        "How long after booking the payment takes economic effect (the value date / settlement delay)",
        "Which ISO 20022 message format the scheme uses internally",
      ],
      answers: [0, 1, 2, 3, 4],
      explanation:
        "Every option but the last names a method on the `Scheme` interface: [[scheme-direction-push]]/[[scheme-direction-pull]] direction, [[settlement-model-net]]/[[settlement-model-gross]] model, [[requires-mandate]], [[allows-return]] and [[settlement-delay]]. Those five are a selection rather than the whole set — the interface carries other axes too, [[scheme-asset|the asset the scheme settles in]] and the kind of address it routes on (`AddressedBy`). ISO 20022 message names (pacs.008, pacs.003) are implementation labels, not scheme-differentiating axes: no method on the interface reports one.",
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
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
        "[[requires-mandate]] schemes validate the mandate at **submission, at the creditor's own bank** — in SEPA the creditor is the party that holds the mandate. If no valid mandate exists the collection is refused there and then, with an error such as `ErrMandateRequired`, and **no debtor leg is ever posted**: a collection posts nothing when it is submitted, and the payer's bank never even receives it. This protects payers from unauthorized debits before any money moves.",
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
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
        "It is posted at the same moment, and in the same transaction, as the debtor leg",
        "It releases funds from the receiving bank's Clearing Suspense into the payee's account",
      ],
      answers: [0, 2, 4],
      explanation:
        "The [[creditor-leg]] is posted at **settlement** (Cleared → Settled). It consists of: Debit Clearing Suspense (releasing the in-flight funds) → Credit payee's deposit. This is distinct from the [[debtor-leg]], which debits the payer's deposit and credits the *payer's own bank's* Clearing Suspense — a separate transaction, in a different bank's book, posted much earlier.",
    },
    {
      kind: "numeric",
      id: "ch11-q20",
      difficulty: "challenge",
      concept: "settlement-model-gross",
      prompt:
        "Bank A sends Bank B $300 and Bank B sends Bank A $100 in one clearing cycle. How many dollars of reserves would move if these payments settled gross (individually) instead of net?",
      answer: 400,
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "Under [[settlement-model-gross]] every payment settles individually: $300 moves one way and $100 moves the other — **$400** of reserves in total. Netting collapses this to just $200, which is exactly why clearing exists as a step before settlement: it converts a large gross flow into a small net one.",
      explore: { label: "See settlement cycles", href: "/clearing-house/cycles" },
    },
    {
      kind: "mc",
      id: "ch11-q21",
      difficulty: "core",
      concept: "unclaimed-balances",
      prompt:
        "A payee closes their account after their bank has accepted an inbound payment but before the cut-off settles. The cycle settles anyway. Where does the money go?",
      options: [
        "Into the closed account, which reopens to receive it",
        "Back to the payer, as an automatic return",
        "To the receiving bank's Unclaimed Balances account, a liability it still owes",
        "It stays in the receiving bank's clearing suspense indefinitely",
      ],
      answer: 2,
      explanation:
        "[[account-status|Closed]] is the one status that refuses a credit, so the payee's bank posts its [[creditor-leg]] to its **[[unclaimed-balances|Unclaimed Balances]]** account instead — a liability, because the bank still owes the money to whoever eventually claims it. The payment still reaches Settled, because it did: the reserves moved and the payee's bank has been paid. It is *not* clearing suspense (option D): that account means \"a leg in flight\", and pooling unapplicable credits there would make one balance answer two questions. What made the check affordable was having somewhere for the money to go, plus the fact that a creditor leg is now the payee's bank's own unit of work — so one payment at one bank fails without taking the cut-off down with it.",
    },
    {
      kind: "truefalse",
      id: "ch11-q22",
      difficulty: "challenge",
      concept: "allows-return",
      prompt:
        "A settled credit transfer is being returned and the payee has already spent the money. Their bank cannot fund the clawback, so it refuses — and no pacs.004 is ever sent. If the same thing happened on a settled direct debit, the biller's bank could refuse in the same way.",
      answer: false,
      explanation:
        "It could not, and the difference is an **ordering** rather than a rule about schemes: [[allows-return|a bank can refuse a leg only if it posts it before it sends]]. The bank that sends the return posts the leg it owns first, so its refusal costs nothing — nothing is posted, no message is built, and the caller is told directly rather than by a `pacs.002`. On a credit transfer that bank is the payee's, holding the clawback, so a spent payee stops the return dead. On a direct debit the returning bank is the *payer's*, and the leg it holds is the refund — always postable. The biller's bank hears about the return only when the clearing house relays the message to it, which is **after** the central bank has already reversed the reserves, so there is nothing left for it to refuse: it forces the clawback, and a biller with a closed account leaves the shortfall in that bank's [[returns-receivable|Returns Receivable]]. That is the credit risk a creditor's bank takes on with every biller it onboards.",
      explore: { label: "Browse payment schemes", href: "/clearing-house/schemes" },
    },
  ],
};
