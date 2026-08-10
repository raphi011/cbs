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
        "[[scheme-direction-pull]] — SDD is a pull scheme whose interbank message is **pacs.003**. The SCT counterpart (a push) uses `pacs.008`. Those two are the *instructions*; the system also implements `pacs.002` (the status a receiver answers with), `pacs.004` (a return), `pacs.009` (the settlement instruction the clearing house sends the central bank) and `camt.053` (the statement of a member's reserve account, which is what that bank books its mirror leg from — after a cut-off and after a return alike). Neither distractor is one of the six: `pain.001` is a customer instructing their own bank rather than an interbank message, and `camt.054` — the advice of an individual credit — is one of the `camt` messages this system does *not* implement.",
    },
    {
      kind: "mc",
      id: "ch12-q4",
      difficulty: "core",
      concept: "mandate",
      prompt:
        "An SDD collection arrives but the debtor revoked the mandate two weeks before the due date. On which grounds is it refused?",
      options: [
        "No mandate exists for this creditor and this account",
        "The mandate that exists has been revoked",
        "The collection exceeds the mandate's cap",
        "The debtor's account has insufficient funds",
      ],
      answer: 1,
      explanation:
        "The [[mandate]] status check fires before any posting, and each ground is a distinct refusal. A mandate that **exists but has been revoked** is refused as revoked — not as missing, which is what a collection against an account that never authorised this creditor gets, and not as over the cap, which is what a collection larger than the mandate's limit gets. Funds are the debtor's bank's business, later and elsewhere: the creditor's own bank never sees that balance.",
      explore: { href: "/", label: "Pick a bank, then Mandates" },
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
        "At initiation, when the payer's bank takes the instruction in",
        "At settlement, when central-bank reserves move",
        "At mandate validation, before any other checks",
      ],
      answer: 1,
      explanation:
        "The [[debtor-leg]] — the entry debiting the payer's deposit account and crediting the bank's clearing suspense — posts at **initiation**, in the payer's own bank, when it submits the credit transfer. Note that the payment is *Initiated* at that point, not Accepted: acceptance is the clearing house's separate act, later. Its two sides value-date differently: the customer's debit value-dates to itself (PSD2 Art. 87(2) forbids dating it any earlier), while the clearing-suspense side value-dates to the settlement date. Clearing later computes net positions; the [[creditor-leg]] is posted at the receiving bank, once settlement has moved the reserves and it has been told so. No debit to the customer's account occurs at clearing itself.",
      explore: { label: "View payments", href: "/clearing-house/payments" },
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
        "The [[creditor-leg]] posts at **settlement**: only after central-bank reserves have moved from the debtor's bank to the creditor's bank does the creditor's bank credit the payee's deposit account out of its [[clearing-suspense]]. Its reserve asset rises in a *separate* posting, booked from the central bank's `camt.053` and arriving first — two units of work from two institutions' messages, not one four-entry posting. The [[debtor-leg]] posts earlier, and always into the *payer's own bank's* clearing suspense — at initiation for a credit transfer, which the payer's bank submits, and when the collection arrives for a direct debit, which the *payee's* bank submits and whose submission posts nothing.",
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
        "Refuses it for exceeding the cap, before any posting",
        "Approves €400 and queues the €50 excess for the next cycle",
        "Automatically updates the mandate cap to €450 and proceeds",
      ],
      answer: 1,
      explanation:
        "The [[mandate]] amount cap is a hard gate, not advisory. Even when every other mandate check passes, a collection exceeding the mandate limit is refused **for exceeding it** before any posting occurs. There is no partial approval or automatic cap increase.",
      explore: { href: "/", label: "Pick a bank, then Mandates" },
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
        "The [[payment-lifecycle]] has a 'Returned' terminal state, reachable *only* after settlement via a SEPA R-transaction. A return is not an undo of the lifecycle — it posts new compensating entries and leaves the original ones intact in the ledger. It is also not one act: the returning bank posts the leg it owns and sends a `pacs.004`, the central bank reverses the reserves between the two banks and is [[settlement-finality|final]] there, and the other bank posts the leg *it* owns when the clearing house relays the message on. The row turns Returned when the second of the two customer legs lands. 'Rejected' is only reachable before settlement.",
      explore: { label: "View payments", href: "/clearing-house/payments" },
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
        "Ledgers are immutable — entries are never deleted. [[allows-return]] — a return works like a [[reversal]] across banks: new **compensating transactions** are posted that offset the original flow. Both the original and compensating entries remain permanently in the ledger history. Note the one thing 'restore the pre-settlement state' would still be wrong about even if entries *could* be deleted: a compensating leg does not always land on the account the original one did. A payer who has closed their account since is refunded into their bank's [[unclaimed-balances|Unclaimed Balances]], and a biller whose account has closed leaves the clawback in its bank's [[returns-receivable|Returns Receivable]]. The books balance; the customers are not always where they started.",
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
      explore: { href: "/", label: "Pick a bank, then Mandates" },
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
        "[[clearing-vs-settlement]] — option A (net positions computed) happens at clearing; option D (the debtor leg) happens in the payer's own bank well before settlement — at initiation for a credit transfer, and when the collection arrives for a direct debit, which is submitted by the *creditor's* bank. At settlement: **reserves move at the central bank** (B), the **creditor leg posts** at the receiving bank (C), and **clearing suspense balances zero out** across both banks (E), completing the flow.",
    },
    {
      kind: "numeric",
      id: "ch12-q18",
      difficulty: "intro",
      concept: "debtor-leg",
      prompt:
        "Alice initiates a €75 SEPA Credit Transfer to Bob. The debtor leg posts at initiation. By how many euros does Alice's book balance decrease? (Enter a number.)",
      answer: 75,
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "The [[debtor-leg]] at initiation debits Alice's deposit account (a Liability) by **€75**, reducing her book balance immediately, while crediting the bank's clearing suspense. The full €75 leaves Alice's balance at initiation — before clearing or settlement occur.",
    },
    {
      kind: "numeric",
      id: "ch12-q19",
      difficulty: "core",
      concept: "settlement-delay",
      prompt:
        "A bank initiates a €60 SEPA Credit Transfer (SCT) and a €40 SEPA Direct Debit (SDD) collection, both at T=0. SCT settles at T+1 and SDD settles at T+2. How many euros of interbank reserves have settled by the end of T+1?",
      answer: 60,
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "[[settlement-delay]] — each SEPA scheme has its own settlement schedule. The SCT (a push) settles at T+1: **€60** of reserves move between banks by end of T+1. The SDD (a pull) doesn't settle until T+2, so its €40 has not yet moved. By T+1, only **€60** of interbank reserves have settled.",
    },
    {
      kind: "numeric",
      id: "ch12-q20",
      difficulty: "challenge",
      concept: "allows-return",
      prompt:
        "A $200 SDD settles successfully. The debtor then triggers a return. After the compensating return transactions post, by how many dollars does the creditor's book balance decrease? (Enter a number.)",
      answer: 200,
      unit: { asset: "USD", in: "major" },
      tolerance: 0,
      explanation:
        "[[allows-return]] — the creditor received $200 when the [[creditor-leg]] posted at settlement, and the clawback takes exactly that back out, reducing the balance by **$200**. The original entries remain in the ledger; only new offsetting entries are added. On a *direct debit* this clawback is the leg the creditor's bank posts **after** the reserves have already moved, so it is forced rather than optional — the biller goes overdrawn if it cannot cover it, since the ledger does not refuse a liability going negative, and if the account has closed the bank funds it out of its own [[returns-receivable|Returns Receivable]] instead. The amount comes out either way; what differs is whose balance sheet absorbs it.",
    },
    {
      kind: "truefalse",
      id: "ch12-q21",
      difficulty: "core",
      concept: "counterparty-details",
      prompt:
        "When a bank submits a pacs.008, it looks up the payee's name in the payee's bank's deposit register before building the message.",
      answer: false,
      explanation:
        "No bank reads another bank's register to build a payment — that crossing is what closed, and it's what this question asks about. The [[counterparty-details|payee's name]] is asserted on the instruction instead: the payer types it, and the submitting bank stores it on the payment exactly as received, refusing to submit at all if it is missing. The submitting bank fills in only its OWN side from its own register — it is the authority on its own customer, never on the other bank's.\n\n**The payee's bank is asserted too.** It is tempting to expect the `CdtrAgt` BIC to be *derived* from the payee's own bank record — name from the payer, routing from the network — but deriving it would mean reading a row belonging to a bank the payer's bank shares no database with. The payer supplies the BIC, exactly as they supply the IBAN, and a missing or malformed one is refused.\n\n**What makes asserting it safe is a second change, and neither works alone.** Address resolution no longer sweeps every bank's register: the receiving bank resolves the address in *its own* register and answers `AC01` when it holds no such account. So a payer who names the wrong bank does not divert the money — that bank simply does not hold the address and refuses. Typing a BIC now chooses which bank you *ask*, not which bank gets paid.",
      explore: { label: "View payments", href: "/clearing-house/payments" },
    },
    {
      kind: "mc",
      id: "ch12-q22",
      difficulty: "challenge",
      concept: "returns-receivable",
      prompt:
        "A biller collects €500 by SEPA Direct Debit. It settles. The payer then exercises the 8-week refund right — and by the time the return reaches the biller's bank, the biller has wound up and closed its account. Who is out of pocket, and how is it recorded?",
      options: [
        "The payer, who cannot be refunded because the money cannot be recovered",
        "The payer's bank, which absorbs it and books the loss to expense",
        "The biller's bank, which funds the refund itself and books a Returns Receivable — an asset, a claim on the biller",
        "Nobody — the return is refused, because a bank cannot post to a closed account",
      ],
      answer: 2,
      explanation:
        "The debtor's 8-week refund right is **unconditional**, so nobody gets to ask whether the biller can still afford it. The payer's bank — the *returning* bank on a pull — posts the refund and sends the `pacs.004` before anything else happens, so the payer is made whole first. By the time the biller's bank hears, the central bank has already reversed the reserves: it cannot refuse (option D), because there is nothing left to refuse. It takes the money out of the biller's account if it can — an overdrawn biller is a debt it collects from a customer it still has — and where the account is **closed** there is nowhere on the account to put the debit, so it funds the clawback itself and books [[returns-receivable|Returns Receivable]]: an [[account-type-asset|asset]], money owed **to** the bank by a party it can name. That is the exact mirror of [[unclaimed-balances|Unclaimed Balances]], which is money the bank owes to a party it cannot. **This is why a creditor's bank vets its creditors** and can demand collateral or an indemnity: it stands behind its biller's collections whether or not the biller is still there.",
      explore: { label: "View payments", href: "/clearing-house/payments" },
    },
  ],
};
