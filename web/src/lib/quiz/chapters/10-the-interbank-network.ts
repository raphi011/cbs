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
      concept: "download-queue",
      prompt:
        "The clearing house has an output file ready for Bank B. How does it reach Bank B?",
      options: [
        "The clearing house opens a connection to Bank B and delivers it",
        "It goes into Bank B's download queue at the clearing house, and stays there until Bank B connects and collects it",
        "It is broadcast to every member, and each bank ignores the files not addressed to it",
        "Bank B is notified, and then requests the file by its reference",
      ],
      answer: 1,
      explanation:
        "**Nothing is ever pushed at a member bank.** The file-transfer protocol between a bank and its clearing house — EBICS, which is what German and French banks actually use — has no push at all: the subscriber is always the client, so a result waits in that subscriber's [[download-queue|download queue]] until it comes and asks.\n\nThe queue is not incidental plumbing. It **is** the routing table: routing a file to Bank B *means* putting it in Bank B's queue, so there is no address to look up and no table of who-is-reachable that could disagree with who-is-a-member — being enrolled is what creates the queue.\n\nAnd it makes a real operational failure expressible for the first time: a bank that never collects has customers who are never told the fate of anything, while its queue grows.\n\n**Not everything a bank collects is queued, and the exception proves what a queue is.** The scheme's [[routing-directory|routing table]] is *published* rather than addressed: one snapshot, offered to every member as the same bytes, so collecting it empties nothing — two members get it and one member may ask twice. A queued file is addressed to one subscriber and is gone once collected, which is why a queue can be a routing table and a published file cannot be a payment.",
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
      answer: false,
      explanation:
        "They are mirror images of the same pile of reserves — Bank A's claim and the central bank's obligation — and they agree **once both institutions have booked**. They do not agree in between, and that gap is designed in, not an error. The central bank posts its netting transaction and is [[settlement-finality|final]] there; it then *tells* Bank A, by `camt.053`, and Bank A books its own side in a [[unit-of-work|unit of work]] of its own. Until it does, the two differ by exactly Bank A's net position. That window is the [[unreconciled-position|unreconciled position]], and the whole point of naming it is that the mismatch is the honest state of a system where no institution writes in another's books.",
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
        "[[clearing-suspense]] is a temporary liability: credited at initiation (Step 1) and debited when Bank A books the cut-off it is told about, leaving a net balance of zero for each settled payment. It returns to zero *after* settlement rather than at it — the central bank commits first and Bank A books its own mirror leg on the `camt.053` it is then sent, so the gap between the two is the [[unreconciled-position|unreconciled position]]. It is a completely separate account from the reserve asset — one captures in-transit obligations; the other is the bank's funded claim on the central bank.",
    },
    {
      kind: "mc",
      id: "ch10-q13",
      difficulty: "core",
      concept: "bulk-file",
      prompt:
        "Bank A uploads one file carrying 400 credit transfers, addressed to customers at three other banks. How many documents does the clearing house produce from it?",
      options: [
        "400 — one instruction relayed onward per transaction",
        "One answer back to Bank A, and three output files — one per receiving bank",
        "Three — one per receiving bank, and Bank A learns the outcome by asking",
        "401 — one answer per transaction plus a summary",
      ],
      answer: 1,
      explanation:
        "One in, one answered, M out. The clearing house answers the whole [[bulk-file|file]] with a **single** `pacs.002` carrying a decision per transaction — whose group status is `PART` when they did not all go the same way — and it **sorts the file by creditor agent**, building one output file per receiving bank.\n\nThat fan-out is what a clearing house is *for*, and it is invisible in any system where a message carries one payment. It does the sort **without reading any record of its own**: a clearing house that had to look a payment up to decide where to send it could not route a file about a payment it does not hold, which in a real network is most of them.\n\nOn a [[scheme-direction-pull|pull]] the sort is by the *debtor's* agent instead — a collection travels towards the money's source — and that one element is the whole difference at this institution.",
    },
    {
      kind: "mc",
      id: "ch10-q14",
      difficulty: "core",
      concept: "payment-hub",
      prompt:
        "A customer instructs a transfer and the bank answers 202 with a payment id. What has actually been sent to the clearing house at that moment?",
      options: [
        "The instruction, as a pacs.008 — the 202 means it is on its way",
        "Nothing. The payment is in that bank's hub, waiting for its next cut-off",
        "A notification that a file will follow, so the clearing house can expect it",
        "The instruction, but held at the clearing house until the payer's bank confirms",
      ],
      answer: 1,
      explanation:
        "Nothing has been sent. The bank has run its **own** half — validated the instruction, posted the [[debtor-leg|debtor leg]] on a push — and put the payment in its [[payment-hub|hub]]: the pile of its customers' instructions waiting for the next [[bulk-file|file]]. The payment reads *Initiated*, is in no cycle, and the payee's bank has never heard of it.\n\nThat is a state it **rests in** rather than passes through, because nothing in a bulk scheme happens on a timer. The **cut-off** is what empties the hub, and everything instructed a moment later waits for the next one.\n\nWhat the synchronous half buys is the refusal: an instruction that fails the submitting bank's own checks — no funds, an account that is not the customer's — is refused there and then, rather than accepted and rejected hours later by a message nobody can answer.",
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
      explore: { label: "See settlement cycles", href: "/clearing-house/cycles" },
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
      explore: { label: "See settlement cycles", href: "/clearing-house/cycles" },
    },
    {
      kind: "numeric",
      id: "ch10-q19",
      difficulty: "challenge",
      concept: "net-positions",
      prompt:
        "In a three-bank clearing cycle: Bank A sends $600 to Bank B; Bank B sends $400 to Bank C; Bank C sends $250 to Bank A. By how many dollars do Bank B's central-bank reserves increase at settlement? (Enter a positive number — Bank B is a net receiver.)",
      answer: 200,
      unit: { asset: "USD", in: "major" },
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
        "Three ledgers move, and they move one after another rather than in lockstep: (1) the **central bank** debits Bank A's reserve liability and credits Bank B's, in its own unit of work, and is [[settlement-finality|final]] there; (2) **Bank A**, told by `camt.053`, closes its [[clearing-suspense]] with a debit and records the outgoing reserves with a credit to its reserve asset; (3) **Bank B**, which collects its own `camt.053` and then the released instruction — in that order, and from two different institutions' queues — debits (raises) its reserve asset and credits Bob's deposit. The entries are the same; the timing is not, and the gap between (1) and the rest is the [[unreconciled-position|unreconciled position]]. Alice's deposit was debited at *initiation* — not at settlement.",
      explore: { label: "View central bank reserves", href: "/central-bank" },
    },
    {
      kind: "mc",
      id: "ch10-q21",
      difficulty: "challenge",
      concept: "allows-return",
      prompt:
        "A settled direct debit is returned. The payer's bank posts the refund and sends the pacs.004 — but the biller's bank has since spent its reserves, so the settlement agent answers RJCT/AM04 and the payer's bank reverses the refund it had already posted. The biller then pays cash in, and the payer asks for the return again. What must the payer's bank do this time?",
      options: [
        "Nothing — its transaction id is already on the payment, so the leg counts as posted and the return can carry on from where it stopped",
        "Post a NEW refund, because reversing the first one means no leg stands — and post it under a different idempotency key, derived from the reversed attempt",
        "Refuse — a return that has been rejected once is terminal, and the payer must be told the money cannot be recovered",
        "Re-post the original refund under the same idempotency key, which the ledger accepts because the first one was reversed",
      ],
      answer: 1,
      explanation:
        "[[allows-return|A return]] refused for `AM04` is refused for a **shortfall**, and a shortfall somebody can cover is not a payer who has lost their refund right — so the return is asked again. What the retry must not do is trust the transaction id left on the payment. The unwind [[reversal|reverses]] the posting and leaves the id in place, because that id is what the retry's key is derived from; the id therefore says *this bank attempted the leg*, never *this bank's leg stands*. Only the transaction's own status answers that. Doing nothing is the defect this rule exists to stop: read as \"already posted\", the retry posts nothing while the conversation runs to completion around it — the reserves reverse, the biller is clawed back, `ACSC` goes back on the wire, and the payer is never repaid. Re-posting under the ORIGINAL key cannot happen either: a ledger refuses a repeated [[idempotency-key|idempotency key]] whatever became of the first posting, which is exactly why the key has to change.",
      explore: { label: "View payments", href: "/clearing-house/payments" },
    },
    {
      kind: "mc",
      id: "ch10-q22",
      difficulty: "challenge",
      concept: "bank-reconciliation",
      prompt:
        "A bank reconciles its own books, reading only its own database and the camt.053 statements it was sent. It finds two things: an entry on its reserve account that is neither a mirror leg nor a lodgement, and a clearing suspense that has held 3000 for nine days. How should it report them?",
      options: [
        "Both as breaks — an unexplained reserve movement and a suspense that has not cleared are equally defects",
        "The reserve entry as a break; the suspense as a position with an age on it, and never as a break",
        "Both as positions — a bank reading one database can never say anything is wrong, only that something is outstanding",
        "The suspense as a break, because nine days is far past the settlement delay; the reserve entry only as a position, since another institution moved it",
      ],
      answer: 1,
      explanation:
        "The two accounts are not symmetric, and the reason is a fact about the postings. Exactly two things post to a bank's [[reserve-account|reserve]]: an advice's **mirror leg**, which a settlement-advice row names the transaction of, and a [[lodgement]], which is the bank's own act. There is no third — so the identity **closes**, every entry is classifiable from inside, and one in neither class is a [[bank-reconciliation|break]] this bank can stand behind. The [[clearing-suspense]] has no such identity and cannot be given one, because the mirror leg is [[netting|netted]]: one cut-off produces one figure per member covering every payment in the batch and naming none of them. So the balance cannot be decomposed into the payments that put it there, and all a bank can honestly say is how old each part of it is — a bank told its payment settled and not told its reserves moved has done nothing wrong, and only the settlement agent's own register separates that from a defect. Nine days is a reason to go and ask, not a finding: no rulebook puts a clock on a clearing suspense, because what discharges it is a conversation.",
    },
  ],
};
