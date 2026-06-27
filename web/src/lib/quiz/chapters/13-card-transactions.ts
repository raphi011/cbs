import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "13-card-transactions",
  number: 13,
  part: "Part IV · Moving Money Between Banks",
  title: "Card Transactions",
  questions: [
    {
      kind: "mc",
      id: "ch13-q1",
      difficulty: "intro",
      concept: "holds",
      prompt:
        "When a card authorization is approved at a point-of-sale terminal, what immediately happens to the cardholder's account?",
      options: [
        "The book balance decreases and the available balance decreases",
        "The book balance is unchanged but the available balance decreases",
        "Both balances are unchanged; no effect occurs until capture",
        "The book balance decreases but the available balance is unchanged",
      ],
      answer: 1,
      explanation:
        "An authorization places a [[holds|hold]] — a real-time reservation. **No posting occurs**, so the [[balance-book|book balance]] is untouched. The available balance falls by the authorized amount so those funds cannot be spent twice while the purchase is in flight.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "mc",
      id: "ch13-q2",
      difficulty: "intro",
      concept: "payment-lifecycle",
      prompt:
        "The dual-message card flow has two distinct steps. What are they, in order?",
      options: [
        "Capture → authorization",
        "Authorization → presentment (capture)",
        "Presentment → clearing → authorization",
        "Clearing → authorization → settlement",
      ],
      answer: 1,
      explanation:
        "The dual-message [[payment-lifecycle]] begins with an **authorization** (hold is placed, no ledger entry) and ends with **presentment** — the merchant's formal claim that triggers [[hold-capture|capture]] into a real posting. These two steps may be hours or days apart.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "truefalse",
      id: "ch13-q3",
      difficulty: "intro",
      concept: "clearing-vs-settlement",
      prompt:
        "The card network (such as Visa or Mastercard) temporarily holds funds between the issuer and acquirer for the duration of the authorization-to-settlement window.",
      answer: false,
      explanation:
        "The card network carries messages and computes net positions but **never holds funds**. It connects the issuer's side to the acquirer's side — money stays in the participants' bank accounts throughout. At the end of the [[clearing-vs-settlement|clearing cycle]], the network tells each bank its net position and the banks settle through reserve-account moves at the central bank.",
    },
    {
      kind: "mc",
      id: "ch13-q4",
      difficulty: "intro",
      concept: "hold-release",
      prompt:
        "A petrol pump pre-authorizes $100, but the customer pumps only $45 of fuel. After the $45 is captured, what happens to the remaining $55 reservation?",
      options: [
        "It is kept on hold until the next billing cycle",
        "It is transferred to the merchant as an automatic gratuity",
        "The $55 hold is released, restoring that amount to available balance",
        "A second capture is automatically raised for $55",
      ],
      answer: 2,
      explanation:
        "When the captured amount is less than the authorized amount, the excess is a [[hold-release]]: the remaining $55 reservation is cancelled and the cardholder's available balance is restored. Only the actual $45 ever reaches the ledger.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "truefalse",
      id: "ch13-q5",
      difficulty: "intro",
      concept: "holds",
      prompt:
        "In a single-message card transaction (such as a PIN debit or ATM withdrawal), authorization and clearing are fused into one step with no separate hold placed first.",
      answer: true,
      explanation:
        "The single-message variant collapses the two-step pattern: the issuer approves *and* commits the debit in one message, so money leaves immediately. There is no [[holds|hold phase]] — the mental model is closer to an instant payment than to the held-then-captured dual-message flow.",
    },
    {
      kind: "mc",
      id: "ch13-q6",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "When a merchant submits a presentment for a card transaction, which accounting entry does the issuer post?",
      options: [
        "Debit Clearing Suspense · Credit Cardholder Deposit",
        "Debit Cardholder Deposit (Liability) · Credit Clearing Suspense (Liability)",
        "Debit Cash (Asset) · Credit Cardholder Deposit",
        "Debit Card Network Payable · Credit Reserve Account",
      ],
      answer: 1,
      explanation:
        "At [[hold-capture]], the issuer posts: **Debit Cardholder Deposit (Liability)** — the cardholder's balance falls for the first time — and **Credit Clearing Suspense (Liability)** — the issuer now owes the network this amount. The hold on available balance is simultaneously removed.",
    },
    {
      kind: "mc",
      id: "ch13-q7",
      difficulty: "core",
      concept: "scheme-direction-pull",
      prompt:
        "Card payments route through a four-party model. Which answer lists all four parties correctly?",
      options: [
        "Cardholder · issuer · central bank · card network",
        "Cardholder · issuer · merchant · acquirer (coordinated by the card network)",
        "Merchant · acquirer · card network · central bank",
        "Cardholder · merchant · clearing house · reserve bank",
      ],
      answer: 1,
      explanation:
        "The [[scheme-direction-pull|card scheme]] routes every transaction through four parties: the **cardholder** (payer), the **issuer** (cardholder's bank), the **merchant** (payee), and the **acquirer** (merchant's bank). The card network sits between issuer and acquirer, routing messages and computing net positions — it never holds the money itself.",
    },
    {
      kind: "numeric",
      id: "ch13-q8",
      difficulty: "core",
      concept: "holds",
      prompt:
        "An account has a book balance of $600 and an existing authorization hold of $200. A card terminal requests a new $150 authorization. If the authorization is approved, how many dollars is the available balance? (Enter a number of dollars.)",
      answer: 250,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Available balance = book balance − total active [[holds]] = $600 − $200 − $150 = **$250**. The [[balance-book|book balance]] is still $600 — no posting has occurred — but $350 in active holds reduces what can be spent or authorized.",
    },
    {
      kind: "mc",
      id: "ch13-q9",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "After a card payment is captured, where does the payment stand in the issuer's books?",
      options: [
        "Fully settled — no further entries are needed",
        "The authorization is reversed and a fresh transaction is initiated",
        "The debtor leg is posted to Clearing Suspense; the payment awaits netting and settlement",
        "The amount sits in the card network's account until end of day",
      ],
      answer: 2,
      explanation:
        "[[hold-capture]] creates the **debtor leg**: the cardholder's deposit is debited and the issuer's Clearing Suspense is credited. From here the payment is ordinary — it nets with other card items across the clearing cycle and settles via a central-bank reserve move, exactly as a SEPA payment would.",
    },
    {
      kind: "truefalse",
      id: "ch13-q10",
      difficulty: "core",
      concept: "account-status",
      prompt:
        "A frozen account blocks card authorizations because an authorization is a debit-side request.",
      answer: true,
      explanation:
        "[[account-status]] frozen prevents all debits and new holds. A card authorization would ultimately produce a debit entry, so the issuer refuses it outright — no hold is placed and the purchase is declined at the terminal, the same outcome as any attempted debit on a frozen account.",
    },
    {
      kind: "mc",
      id: "ch13-q11",
      difficulty: "core",
      concept: "balance-available",
      prompt:
        "Which balance does an issuer examine when deciding whether to approve a card authorization?",
      options: [
        "The book balance, which records every posted transaction",
        "The available balance, which deducts active holds from the book balance",
        "The clearing suspense balance, which tracks in-flight payments",
        "The merchant's acquirer credit limit",
      ],
      answer: 1,
      explanation:
        "The issuer checks the [[balance-available|available balance]] — not the book balance — before approving a card authorization. The available balance already deducts all active holds, so a cardholder cannot spend funds that are reserved by another pending authorization. Only if the available balance is sufficient does the issuer approve and place a new hold.",
    },
    {
      kind: "multi",
      id: "ch13-q12",
      difficulty: "core",
      concept: "payment-lifecycle",
      prompt:
        "Which of the following statements are true of the dual-message card payment lifecycle? Select all that apply.",
      options: [
        "Authorization places a hold on available balance and makes no ledger posting",
        "The book balance falls at authorization, before any capture",
        "Presentment converts the hold into a real accounting entry on the ledger",
        "The card network holds the money between authorization and capture",
        "The capture amount must exactly equal the authorized amount",
      ],
      answers: [0, 2],
      explanation:
        "In the [[payment-lifecycle]] dual-message flow: authorization places a [[holds|hold]] — no posting, only available balance moves — and presentment then converts that hold into the real debtor-leg posting. Book balance does not fall until capture. The network routes messages but never holds funds. The capture amount may legitimately differ from the authorization (tip additions, partial captures, gas pump true-up).",
    },
    {
      kind: "mc",
      id: "ch13-q13",
      difficulty: "core",
      concept: "clearing-vs-settlement",
      prompt:
        "Once a card payment is captured, how does it complete its journey through the interbank system?",
      options: [
        "The card network holds the funds and transfers them directly to the acquirer at end of day",
        "Each card transaction settles gross and in real time via the central bank",
        "The captured debtor leg nets across the clearing cycle and settles through the same reserve-movement machinery used by SEPA",
        "The issuer wires funds directly to the merchant's deposit account",
      ],
      answer: 2,
      explanation:
        "After capture, a card payment is structurally identical to any other interbank credit. The [[clearing-vs-settlement|clearing and settlement]] machinery — netting, clearing suspense, central-bank reserve moves — applies unchanged. Card networks compute each bank's net position over a cycle and settle the net, reusing the existing rails rather than inventing new ones.",
      explore: { label: "View settlement cycles", href: "/cycles" },
    },
    {
      kind: "numeric",
      id: "ch13-q14",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "A restaurant authorizes $50 for a meal. The customer writes in a $14 tip and the final capture is $64. By how many dollars does the cardholder's book balance fall at capture? (Enter a number of dollars.)",
      answer: 64,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "[[hold-capture]] posts the **final captured amount** — $64 — to the ledger, not the original authorization of $50. Card schemes permit captures to exceed the authorized amount within a reasonable tolerance (such as a tip). The original $50 hold is released and the book balance falls by exactly **$64**.",
    },
    {
      kind: "multi",
      id: "ch13-q15",
      difficulty: "core",
      concept: "balance-holds",
      prompt:
        "Which statements about card authorization holds are correct? Select all that apply.",
      options: [
        "A hold reduces the available balance but leaves the book balance unchanged",
        "An active hold is recorded as a debit entry in the general ledger",
        "If a hold expires before capture, the available balance is automatically restored",
        "Releasing a hold triggers a reversal transaction in the ledger",
        "A hold only becomes a ledger entry when it is captured",
      ],
      answers: [0, 2, 4],
      explanation:
        "[[balance-holds]] live in the deposit layer, not the ledger. They reduce [[balance-available]] without posting anything. A released or expired hold produces no ledger entry — from the ledger's perspective it never happened. Only [[hold-capture]] converts a hold into a real posting.",
    },
    {
      kind: "mc",
      id: "ch13-q16",
      difficulty: "challenge",
      concept: "hold-release",
      prompt:
        "A card authorization for $200 is placed on an account. The merchant never submits a presentment, and the authorization window expires. What is the outcome?",
      options: [
        "The $200 is automatically captured and posted to the cardholder's account",
        "The hold expires, the $200 is restored to available balance, and no ledger entry is ever posted",
        "The $200 remains held permanently until the cardholder contacts their bank",
        "The expired authorization converts to a formal debit that the merchant must later reverse",
      ],
      answer: 1,
      explanation:
        "[[hold-release]] by expiry: holds carry a finite authorization window. If the merchant does not capture within that window, the hold lapses automatically — the reserved $200 is restored to available balance and no posting ever reaches the ledger. From an accounting standpoint the authorization left no trace.",
    },
    {
      kind: "numeric",
      id: "ch13-q17",
      difficulty: "challenge",
      concept: "hold-release",
      prompt:
        "An account has a book balance of $800 and an existing $100 authorization hold from an earlier transaction. A petrol pump pre-authorizes $150. The customer pumps $120 of fuel; the $120 is captured and the excess hold released. How many dollars of available balance does the account have immediately after the capture? (Enter a number of dollars.)",
      answer: 580,
      unit: "dollars",
      tolerance: 0,
      explanation:
        "Before capture: available = $800 book − $100 existing hold − $150 pump hold = **$550**. At capture, the $120 is posted (book falls to $680) and the full $150 pump hold is [[hold-release|released]] — only the $100 existing hold remains. Available = $680 − $100 = **$580**. The $30 excess reservation disappears without a ledger trace.",
    },
    {
      kind: "multi",
      id: "ch13-q18",
      difficulty: "challenge",
      concept: "payment-lifecycle",
      prompt:
        "A hotel pre-authorizes $300 for a 3-night stay. The guest later orders $60 of room service and the hotel adds an incremental authorization. Which statements correctly describe the situation before final capture? Select all that apply.",
      options: [
        "The available balance is reduced by $300 from the initial authorization",
        "The book balance has already fallen by $300",
        "An incremental authorization can raise the total held amount above the original $300",
        "Only one authorization hold can be active on an account at a time",
        "The final capture will post only the original $300 regardless of incremental authorizations",
      ],
      answers: [0, 2],
      explanation:
        "In the incremental [[payment-lifecycle]] pattern, the initial authorization places a $300 [[holds|hold]] on available balance — the [[balance-book|book balance]] is untouched. An incremental authorization raises the total reservation above $300 to cover the room service. The final [[hold-capture|capture]] posts the actual billed amount, which may differ from any intermediate authorized figure.",
    },
    {
      kind: "mc",
      id: "ch13-q19",
      difficulty: "challenge",
      concept: "settlement-delay",
      prompt:
        "A cardholder makes a card purchase at noon on a Tuesday. According to typical card settlement timelines, when will the merchant's acquirer most likely receive the funds?",
      options: [
        "Immediately, as soon as the authorization is approved",
        "By end of Tuesday (same-day settlement)",
        "One to two business days after the transaction (T+1 to T+2)",
        "Three to five business days later, matching check deposit timelines",
      ],
      answer: 2,
      explanation:
        "[[settlement-delay]] for card transactions is typically T+1 to T+2 — the merchant's bank receives funds one to two business days after the sale. This gap exists because captured items net across a clearing cycle before the interbank settlement move is made at the central bank. The cardholder sees an immediate hold at authorization, then a posted debit after capture (presentment).",
    },
    {
      kind: "truefalse",
      id: "ch13-q20",
      difficulty: "challenge",
      concept: "clearing-vs-settlement",
      prompt:
        "After card transactions are captured, the card network settles each transaction individually in real time — unlike SEPA, which nets positions across a clearing cycle.",
      answer: false,
      explanation:
        "Card settlement works the same way as SEPA: the network [[clearing-vs-settlement|computes each bank's net position]] across a clearing cycle and settles the net. Individual card transactions are not settled gross in real time. The clearing-suspense entries from captured transactions drain or fill through the same reserve-movement machinery Chapters 9–11 describe — cards reuse the existing rails rather than inventing new ones.",
    },
  ],
};
