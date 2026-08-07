import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "15-persistence-and-durability",
  number: 15,
  part: "Part VI · Making It Durable",
  title: "Persistence, Transactions, and Durability",
  questions: [
    {
      kind: "mc",
      id: "ch15-q1",
      difficulty: "intro",
      concept: "relational-mapping",
      prompt:
        "When the ledger is mapped onto relational tables, how is a multi-legged transaction stored?",
      options: [
        "One row per transaction, with the legs serialized into a JSON column",
        "A parent row in `transactions` and one child row per leg in `entries`",
        "One row per leg, with the transaction's fields repeated on each row",
        "Two rows per transaction — one for the debit total and one for the credit total",
      ],
      answer: 1,
      explanation:
        "A transaction is a parent row and its legs are child rows in `entries`. This is the [[relational-mapping|natural relational shape]] of [[double-entry]]: 'a posting has two or more balanced entries' is a one-to-many relationship. Serializing the legs into JSON would make it impossible to index or sum entries by account, which is exactly what a balance query needs.",
    },
    {
      kind: "truefalse",
      id: "ch15-q2",
      difficulty: "intro",
      concept: "derived-balance",
      prompt:
        "The `accounts` table has a `balance` column that is updated on every posting.",
      answer: false,
      explanation:
        "There is no balance column. A [[derived-balance|balance is derived]] by summing the account's entries, signed by [[normal-balance]]. A stored balance is a cache of a derivable fact, and any write that updated one without the other would leave a number nobody could reconcile against the [[audit-trail|history]].",
    },
    {
      kind: "mc",
      id: "ch15-q3",
      difficulty: "intro",
      concept: "unit-of-work",
      prompt: "What is a 'unit of work'?",
      options: [
        "One atomic scope — everything inside it commits together or rolls back together",
        "The batch of payments collected into a single clearing cycle",
        "A single SQL statement, which a database always runs atomically",
        "The set of changes one background worker process is responsible for",
      ],
      answer: 0,
      explanation:
        "A [[unit-of-work]] is `BEGIN … COMMIT`, or `ROLLBACK` and it is as if nothing happened. It is not one statement — the interesting operations here span many statements across all three layers. (A clearing cycle is a business-level batch; it is a different idea, and only the central bank's half of it settles *inside* one unit of work.)",
    },
    {
      kind: "truefalse",
      id: "ch15-q4",
      difficulty: "intro",
      concept: "book-scoped-id",
      prompt:
        "Two different participant banks can each hold a general-ledger account numbered 200.100.001 without any collision.",
      answer: true,
      explanation:
        "Chart-of-accounts numbers are unique within a book, not globally, so [[book-scoped-id|the primary key is the pair `(book_id, id)`]]. Every bank keeps its own book with its own numbering — forcing globally unique numbers would destroy the [[ledger-vs-subledger|chart of accounts]] as a readable per-bank structure.\n\nSince the [[store-split|store split]] there is a second, blunter reason the two cannot collide: each bank's book is in its **own database**, so Bank A's `200.100.001` and Bank B's are not two rows in one table at all. The composite key survives anyway — `book_id` simply takes one value per database now — because a book id is a real key rather than a convention.",
    },
    {
      kind: "multi",
      id: "ch15-q5",
      difficulty: "intro",
      concept: "snapshot",
      prompt:
        "Which of the following are stored as their own tables when the system is persisted to a relational database? (Select all that apply.)",
      options: [
        "Transactions and their entries",
        "Deposit accounts and holds",
        "End-of-day snapshots",
        "Audit events",
        "Each account's current balance",
      ],
      answers: [0, 1, 2, 3],
      explanation:
        "Everything the domain actually stores gets a table: transactions, entries, deposit accounts, [[holds]], [[snapshot|snapshots]] and [[audit-trail|audit events]]. A balance is not stored at all — it is [[derived-balance|computed from the entries]] on demand.",
    },
    {
      kind: "mc",
      id: "ch15-q6",
      difficulty: "core",
      concept: "derived-balance",
      prompt:
        "What is the main cost of deriving balances instead of storing them, and what is the standard remedy?",
      options: [
        "Reads become an aggregate over every entry ever posted; the remedy is to checkpoint with end-of-day snapshots",
        "Writes become slower; the remedy is to batch postings and apply them nightly",
        "Balances can drift from the ledger; the remedy is a nightly reconciliation job",
        "Rounding errors accumulate; the remedy is to store amounts as decimals",
      ],
      answer: 0,
      explanation:
        "The cost is read cost — the sum walks every entry on the account. The remedy is not to add the column back but to checkpoint: a query would start from the nearest checkpoint and replay only what came after, and an [[snapshot|end-of-day snapshot]] records a day's figure so that it can. That remedy is described and not built here — no balance query in this system consults a snapshot. Drift is what [[derived-balance|deriving]] *prevents*, and amounts stay [[minor-units|integer minor units]] either way.",
    },
    {
      kind: "numeric",
      id: "ch15-q7",
      difficulty: "core",
      concept: "normal-balance",
      prompt:
        "An account's `entries` rows are: a debit of 500, a credit of 200, a debit of 150, and a credit of 50 (all in cents). The account is an asset, so its normal balance is debit. What does the derived book balance query return, in cents?",
      answer: 400,
      unit: { asset: "EUR", in: "minor" },
      tolerance: 0,
      explanation:
        "The [[derived-balance|balance query]] sums debits minus credits for a debit-normal account: 500 − 200 + 150 − 50 = **400**. Nothing is read from an account row; the figure exists only as the result of that aggregate. See [[normal-balance]] for why the sign flips for a liability.",
    },
    {
      kind: "truefalse",
      id: "ch15-q8",
      difficulty: "core",
      concept: "audit-trail",
      prompt:
        "An audit event is written inside the same database transaction as the operation it describes.",
      answer: true,
      explanation:
        "The [[audit-trail|audit event]] shares the operation's [[unit-of-work]], so an operation that rolls back leaves no event claiming it happened. Writing the log outside the transaction — or after the commit — produces exactly the two failures a log exists to rule out: a recorded event that never occurred, and an occurrence with no record.",
    },
    {
      kind: "mc",
      id: "ch15-q9",
      difficulty: "core",
      concept: "unit-of-work",
      prompt:
        "The central bank's settlement of a clearing cycle debits every net payer's reserve account, credits every net receiver's, and marks the cycle settled. Why must all of that be one unit of work?",
      options: [
        "Because a partial success would leave money that had left one bank without arriving at another",
        "Because the database can only hold one connection open at a time",
        "Because netting is only mathematically valid inside a transaction",
        "Because the audit trail requires events to carry consecutive sequence numbers",
      ],
      answer: 0,
      explanation:
        "A settlement window is atomic or it is broken. If a net payer's reserve were debited and a net receiver's never credited, central-bank money would have left one bank without reaching another, and no later retry could tell which half had happened. One [[unit-of-work]] makes 'all of it or none of it' a property of the code rather than a hope. Note what it does **not** span: each member's own [[creditor-leg|creditor leg]] and reserve mirror are that bank's own units of work, posted when it is told, because no institution's transaction may reach into another's books.",
    },
    {
      kind: "multi",
      id: "ch15-q10",
      difficulty: "core",
      concept: "row-locking",
      prompt:
        "A single process-wide mutex made every operation in the in-memory store atomic and isolated for free. Which read-then-write races does a real database have to close explicitly once that mutex is gone? (Select all that apply.)",
      options: [
        "A balance check followed by the posting that depends on it",
        "An idempotency-key check followed by the insert",
        "A reversal that reads a transaction's status and then writes the new one",
        "Reading a completed end-of-day snapshot for a past date",
      ],
      answers: [0, 1, 2],
      explanation:
        "Three races were hidden by the mutex: the balance check ([[row-locking|closed by locking, or by a store that refuses the loser's write]]), the [[idempotency-race|idempotency key]] (closed with a unique index), and the reversal (closed with a conditional update). Reading a finished snapshot for a past date is a pure read of immutable data — nothing races with it.",
    },
    {
      kind: "mc",
      id: "ch15-q11",
      difficulty: "core",
      concept: "row-locking",
      prompt:
        "A store that protects a balance check by locking takes `SELECT id FROM accounts WHERE … ORDER BY id FOR UPDATE`. Why the `ORDER BY id`?",
      options: [
        "So the locked rows come back in a predictable order for the caller to iterate",
        "So two transactions over overlapping account sets take their locks in the same order and cannot deadlock each other",
        "Because `FOR UPDATE` is only permitted on an ordered query",
        "To make the lock cover the accounts in chart-of-accounts order, which the ledger requires",
      ],
      answer: 1,
      explanation:
        "Two transactions locking the same accounts in *different* orders would each hold a row the other wants — a deadlock. Taking [[row-locking|the locks in one agreed order]] means the second one simply waits. It is a two-word change that turns a hang into a queue. A store that instead *detects* the conflict — one writer at a time, and the loser's write refused so the whole unit of work runs again — needs no order, because it takes no locks.",
    },
    {
      kind: "mc",
      id: "ch15-q12",
      difficulty: "core",
      concept: "idempotency-race",
      prompt:
        "Why is checking whether an idempotency key exists and then inserting the transaction not a correct implementation under concurrency?",
      options: [
        "Because the SELECT and the INSERT may target different database connections",
        "Because two concurrent retries can both find nothing and both insert",
        "Because the check reads uncommitted data and may see a phantom row",
        "Because the key may be reused legitimately by a different logical operation",
      ],
      answer: 1,
      explanation:
        "There is a window between looking and acting, and two retries of the same request fit inside it — so the duplicate the [[idempotency-key]] exists to prevent gets posted twice. The fix is to make the check and the write [[idempotency-race|one statement]]: attempt the insert against a unique index and translate the resulting violation into the domain error.",
    },
    {
      kind: "truefalse",
      id: "ch15-q13",
      difficulty: "core",
      concept: "relational-mapping",
      prompt:
        "The `entries` table needs an explicit position column, because a transaction's legs are an ordered list and a table has no inherent row order.",
      answer: true,
      explanation:
        "A relational table is a set: without an ordering column, the legs come back in whatever order the query plan produces. Since a transaction's entries are an ordered slice — and the order is visible on statements and in settlement postings — [[relational-mapping|the position is stored]] rather than inferred.",
    },
    {
      kind: "mc",
      id: "ch15-q14",
      difficulty: "core",
      concept: "relational-mapping",
      prompt:
        "Why are listings ordered by `(created_at, seq)` rather than by the row's ID?",
      options: [
        "Because IDs are counter-derived strings, so `dep_10` sorts before `dep_8` and a list silently reorders itself once a counter crosses a power of ten",
        "Because IDs are randomly generated and carry no ordering information at all",
        "Because sorting by a text column is slower than sorting by a timestamp",
        "Because IDs are reused after a row is deleted, making them ambiguous",
      ],
      answer: 0,
      explanation:
        "[[relational-mapping|Ordering by ID is a lexicographic sort of a numeric counter]]: `dep_10` < `dep_8` as text. The list looks right until the ninth account is opened. `seq` is a monotonic integer assigned on insert and deliberately left alone by updates, so editing a row does not move it to the end of its list.",
    },
    {
      kind: "mc",
      id: "ch15-q15",
      difficulty: "core",
      concept: "routing-roster",
      prompt:
        "Admitting a bank writes three rows — the bank's own, the central bank's record of the account it opened, and the clearing house's routing entry — rather than one `participants` row carrying all of it. What forces the split?",
      options: [
        "Row width: one row carrying every institution's fields had grown past what the store could index",
        "The settlement agent resolved the account it was about to post to through a row it did not own, so an institution given a database of its own would have had nothing to settle from",
        "A composite primary key cannot span three institutions in Postgres, and the in-memory store could not express one either",
        "Each institution needed its own copy of the same data, so the row is now duplicated three ways",
      ],
      answer: 1,
      explanation:
        "One row belonging to three institutions is a shared-store artefact, and the reserve postings were reading straight through it. The three rows are in three **different databases** now (see [[store-split]]), so the old read is not merely discouraged — the settlement agent has no `banks` table to make it against. Splitting it gives each institution a record it could hold **alone**, which is the whole test: `banks` is the bank's own (its status, and the admission it accepted), `settlement_members` is the [[settlement-account|central bank's record of an account it opened]], and `roster_entries` is the [[routing-roster|clearing house's record of where to send a message]]. Each has exactly one writer. The two that are not the bank's are keyed by **BIC** rather than by a bank id, because neither institution allocates or is ever told one — what a message carries is a BIC — so a bank id there would be an identifier its owner had no way to have learnt and no way to check. The answer about **each institution needing its own copy of the same data** is the opposite of what happened: the rows hold *different* things, and the clearing house's deliberately holds no account identifier at all.",
    },
    {
      kind: "multi",
      id: "ch15-q16",
      difficulty: "challenge",
      concept: "unit-of-work",
      prompt:
        "Opening a nested unit of work inside an existing one is refused outright rather than allowed. What would go wrong if it were permitted? (Select all that apply.)",
      options: [
        "The inner scope would be a separate transaction, so its writes could commit even when the outer scope rolls back",
        "Each nested scope takes another connection from the pool, so deep nesting can exhaust it and deadlock",
        "The inner scope would not see the outer scope's uncommitted writes, so it could act on stale state",
        "The database would silently reorder the statements in the outer scope",
      ],
      answers: [0, 1, 2],
      explanation:
        "All three are real. This is why every mutating method comes in a pair: the plain one opens a [[unit-of-work]], the `…Tx` one joins the caller's. Refusing the nesting turns the most likely mistake in this codebase — calling the wrong one of the pair — into an immediate, named error rather than a rare corruption whose shape depends on what is underneath.",
    },
    {
      kind: "mc",
      id: "ch15-q17",
      difficulty: "challenge",
      concept: "idempotency-race",
      prompt:
        "A reversal is implemented as `UPDATE transactions SET status = 'Reversed' WHERE id = ? AND status = 'Posted'`, and the outcome is decided by the number of rows affected. What does this buy over reading the status, comparing it, and then writing?",
      options: [
        "It avoids a second round trip to the database, which makes reversals faster",
        "The read and the write are one statement, so two concurrent reversals cannot both see 'Posted' and both proceed",
        "It lets the reversal be retried safely, because the update is idempotent",
        "It removes the need to hold a row lock, because updates never block each other",
      ],
      answer: 1,
      explanation:
        "Read-compare-write leaves the same window as a check-then-insert: two concurrent reversals both read `Posted`, both write, and the transaction is reversed twice. Folding the condition into the `WHERE` clause makes [[idempotency-race|the write itself the decision]] — zero rows affected means somebody else got there first. (It is emphatically *not* idempotent in the retry sense: a second call correctly reports that it did nothing.)",
    },
    {
      kind: "truefalse",
      id: "ch15-q18",
      difficulty: "challenge",
      concept: "derived-balance",
      prompt:
        "Because a reversal is a new, equal-and-opposite posting rather than a deletion, the derived balance sums both the original transaction's entries and the reversal's.",
      answer: true,
      explanation:
        "[[reversal|A reversal never removes anything]] — the ledger is append-only, so both sets of entries stay in the table and the sum nets them to zero. This is why the [[derived-balance|balance query]] does not filter on transaction status: excluding reversed transactions would double-count the correction and produce a balance no statement could reconcile.",
    },
    {
      kind: "mc",
      id: "ch15-q19",
      difficulty: "challenge",
      concept: "book-scoped-id",
      prompt:
        "Every query against a book-scoped table carries `WHERE book_id = ?`. What is the failure mode when one is accidentally omitted?",
      options: [
        "The query errors, because a composite primary key requires both columns",
        "The query returns nothing, because the remaining predicate cannot match",
        "The query silently succeeds and returns another bank's rows alongside this one's",
        "The query is rejected by the foreign key on `book_id`",
      ],
      answer: 2,
      explanation:
        "This is the quiet one. A missing book scope is not a syntax error and not an empty result — it is a [[book-scoped-id|correct-looking query over the wrong set of rows]], and since two banks legitimately share account numbers, the extra rows look plausible. It surfaces as one bank's customer appearing in another bank's listing, long after the code shipped.",
    },
    {
      kind: "numeric",
      id: "ch15-q20",
      difficulty: "challenge",
      concept: "reversal",
      prompt:
        "A three-legged transaction is posted and then reversed. Counting only these two transactions, how many rows exist in the `entries` table?",
      answer: 6,
      tolerance: 0,
      explanation:
        "Three entries for the original and three for the [[reversal]], so **6**. The reversal is a separate parent row in `transactions` with its own children; nothing is edited or deleted. The original's status becomes `Reversed` for reporting, but its entries stay exactly as posted — which is what lets the [[audit-trail|history]] be replayed to recompute any past balance.",
    },
    {
      kind: "mc",
      id: "ch15-q21",
      difficulty: "challenge",
      concept: "unreconciled-position",
      prompt:
        "`settlement_advices` is a member bank's record of a reserve movement it was told about. Its `reference` column has no foreign key to anything. Why is that deliberate?",
      options: [
        "Because a foreign key would be too slow on a table this large",
        "Because the cycle may not have been created yet when the advice arrives",
        "Because the reference names a cycle or a payment row in an institution the member does not share a database with, in either direction",
        "Because `reference` is nullable, and a foreign key cannot reference a nullable column",
      ],
      answer: 2,
      explanation:
        "Each institution holds only what its own job needs. A cycle is the clearing house's row and a settlement is the central bank's; a bank never reads either. It learns of a cut-off from the `camt.053` addressed to it and from one `pacs.002` per payment, and its own record is this row, keyed `(book_id, reference, asset)` — the first payment-layer table **keyed by** book. (`banks` carries a `book_id` column too, but as data — which book that bank owns — rather than as part of its key.) `reference` holds a cycle id on the cut-off path and a payment id when a single settled payment is returned, and a member bank can no more resolve one than the other. A foreign key would encode exactly the sharing that per-entity stores remove. Two banks advised of the same movement write their rows independently, which is not redundancy: [[settlement-finality|settlement is final]] at the central bank, so \"this bank has booked it and that one has not\" is a state the schema must be able to represent — as one row present and the other **absent**, since each row commits with the mirror leg it records. See [[unreconciled-position]].",
    },
    {
      kind: "mc",
      id: "ch15-q22",
      difficulty: "challenge",
      concept: "unreconciled-position",
      prompt:
        "A member bank's `settlement_advices.reference` holds a cycle id when the movement came from a cut-off and a payment id when it came from a single returned payment. There is deliberately no `kind` column beside it saying which. Why not?",
      options: [
        "Because the bank would have to keep it in step by hand, and the two kinds of id are indistinguishable anyway",
        "Because ids are unique across the store, a member can resolve neither kind anyway, and a reconciliation reads one shape rather than two — so the column would be a field nothing reads",
        "Because adding it would break the shared store suite, which pins the row's shape",
        "Because the kind is already implied by the sign of the movement column",
      ],
      answer: 1,
      explanation:
        "The row means *this bank booked this movement*, and every reader of it wants the same thing whichever route the movement arrived by: whether the [[clearing-suspense|suspense]] has cleared against something the bank was told. A member cannot look a cycle up and cannot look the payment up either — both belong to institutions it does not share a database with — so knowing which one it is buys nothing it could act on. The reconciliation that will eventually read these rows ([[unreconciled-position|the absence of one against a suspense that has not cleared]]) reads one shape, and a discriminator nothing branches on is a column that can only ever drift out of step with the id beside it. The answer calling the two kinds of id **indistinguishable** gets the conclusion right for the wrong reason, and is false where it matters: the two ids are perfectly distinguishable — they carry different prefixes — and the column is unnecessary because nothing would *branch* on the answer, not because the answer is unavailable.",
      explore: { label: "View settlements", href: "/clearing-house/settlements" },
    },
  ],
};
