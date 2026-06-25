# Where do a deposit account's ledger entries live?

> **The one-sentence answer:** a deposit account's entries are **not stored
> separately**. There is a single shared transaction log; an account's
> "statement" is just the entries in that log *filtered* by account id, and its
> balance is those same entries *summed*. Nothing is copied, and no balance is
> ever stored.

This document builds that answer up from scratch, shows how *this* codebase
implements it, contrasts it with how legacy banks do it, and explains the
integrity guarantee that holds it all together.

---

## 1. The question, stated precisely

When you look at one customer's checking account you see a list of transactions
and a balance. The natural question is: **does that list physically exist as the
account's own private ledger, or is it produced on demand from a bigger shared
store?**

Two possible designs:

| Design | Where the entries live | The account's statement is… |
| --- | --- | --- |
| **Stored separately** | Each account owns a private list of its entries | …read directly from that private list |
| **Filtered** | One shared log holds everyone's entries, each tagged with an account id | …a query: "give me entries where `account = X`" |

This system uses the **filtered** design — and, as we'll see, takes it further
than most: it doesn't even store the *balance*, it recomputes it from the log
every time.

---

## 2. The vocabulary (and the one distinction that causes all the confusion)

The thing that trips people up is that there are **two different "accounts"**:

- **GL account** (`ledger.Account`) — a pure accounting account. This is where
  money and entries actually live. It has a *type* (Asset, Liability, …) and
  belongs to a subledger.
- **Deposit account** (`deposit.Account`) — the customer-facing object. It holds
  *operational* state (status, overdraft limit) and a **pointer** to one GL
  account. It stores **no money and no entries of its own.**

A customer's money is the balance of the **Liability** GL account that their
deposit account points at. (Liability, because the customer's deposit is money
the *bank owes them* — see `book/03-the-chart-of-accounts.md`.)

```go
// deposit/types.go:53
type Account struct {
    ID             AccountID
    GLAccount      ledger.AccountID // ← the customer's money lives HERE, in the GL
    Name           string
    Status         AccountStatus    // Active / Dormant / Frozen / Closed
    OverdraftLimit ledger.Amount
    CreatedAt      time.Time
}
```

So "deposit account vs subledger" is really: **a thin operational wrapper
pointing at one GL account, which is filed inside a subledger.**

---

## 3. How the pieces relate

```mermaid
erDiagram
    LEDGER          ||--o{ SUBLEDGER       : contains
    SUBLEDGER       ||--o{ GL_ACCOUNT       : groups
    DEPOSIT_ACCOUNT ||--|| GL_ACCOUNT       : "wraps 1-to-1"
    DEPOSIT_ACCOUNT ||--o{ HOLD             : "has"
    TRANSACTION     ||--|{ ENTRY            : "bundles 2+ balanced legs"
    GL_ACCOUNT      ||--o{ ENTRY            : "referenced by"

    LEDGER {
        string ID
        string Name
    }
    SUBLEDGER {
        string ID
        string Name "just a folder — no balance"
    }
    GL_ACCOUNT {
        string ID
        string Type "Asset / Liability / ..."
        string SubledgerID FK
    }
    DEPOSIT_ACCOUNT {
        string ID
        string GLAccount FK "pointer to its GL account"
        string Status
        int OverdraftLimit
    }
    ENTRY {
        string ID
        string AccountID FK "which GL account this leg hits"
        int Amount
        string Direction "Debit or Credit"
    }
    TRANSACTION {
        string ID
        date BookingDate
        string Status
    }
    HOLD {
        string ID
        int Amount
        string Status "lives OUTSIDE the ledger"
    }
```

Read the two key edges carefully:

- `TRANSACTION ||--|{ ENTRY` (solid line): entries are **owned by** a transaction
  — they're stored as a slice *inside* it (`ledger/types.go:154`). An entry has no
  independent existence.
- `GL_ACCOUNT ||--o{ ENTRY` (the "referenced by" edge): an account does **not**
  own a list of entries. Entries merely *carry* an `AccountID` tag
  (`ledger/types.go:129`). To find an account's entries you scan the log for that
  tag.

That second point **is** the answer to your question, in the data model.

---

## 4. The structural hierarchy

```mermaid
flowchart TD
    GL["General Ledger<br/>(ledger.Ledger)"]
    SUBDEP["Customer Deposits<br/>(subledger — a folder)"]
    SUBASSET["Bank Assets<br/>(subledger — a folder)"]
    ALICE["Alice Checking<br/>(GL account · Liability)"]
    BOB["Bob Checking<br/>(GL account · Liability)"]
    VAULT["Cash Vault<br/>(GL account · Asset)"]
    DA["Alice's deposit account<br/>status, overdraft, holds"]

    GL --> SUBDEP
    GL --> SUBASSET
    SUBDEP --> ALICE
    SUBDEP --> BOB
    SUBASSET --> VAULT
    DA -.->|"points at"| ALICE

    classDef folder fill:#eef,stroke:#88a
    classDef wrapper fill:#efe,stroke:#8a8
    class SUBDEP,SUBASSET folder
    class DA wrapper
```

Note what the **subledger is not**: it has no balance field and no list of
entries (`ledger/types.go:109` — it's just `{ID, LedgerID, Name, CreatedAt}`). It
is purely a label that groups GL accounts. "All customer deposits" means "every GL
account whose `SubledgerID` is the Customer Deposits folder."

---

## 5. A worked example — follow the money

Three events. Watch where data lands.

1. **Alice deposits $100 cash.** Double-entry: the bank's cash goes up *and* the
   bank's debt to Alice goes up.
   - Debit `Cash Vault` (Asset) 100
   - Credit `Alice Checking` (Liability) 100
2. **Bob deposits $50 cash.**
   - Debit `Cash Vault` 50
   - Credit `Bob Checking` 50
3. **Alice withdraws $30 cash.**
   - Debit `Alice Checking` 30
   - Credit `Cash Vault` 30

After these, here is **the entire stored state** — one flat log of entries, each
tagged with the GL account it hits. There is no per-account storage anywhere:

```mermaid
flowchart TD
    subgraph LOG["The one transaction log — every entry, stored exactly once"]
        direction TB
        t1["tx1 · Alice Checking · +100 · Credit"]
        t1b["tx1 · Cash Vault · 100 · Debit"]
        t2["tx2 · Bob Checking · +50 · Credit"]
        t2b["tx2 · Cash Vault · 50 · Debit"]
        t3["tx3 · Alice Checking · 30 · Debit"]
        t3b["tx3 · Cash Vault · 30 · Credit"]
    end
```

Now Alice's **statement** and **balance** are both *derived* from that log — they
are not stored:

```mermaid
flowchart LR
    LOG[("Transaction log<br/>(all entries)")]
    STMT["Alice's statement<br/>= the 2 entries tagged 'Alice Checking'<br/>(+100 credit, 30 debit)"]
    BAL["Alice's balance<br/>= sum of those entries<br/>Liability: +100 − 30 = 70"]
    BOBSTMT["Bob's statement<br/>= the 1 entry tagged 'Bob Checking'"]

    LOG -->|"filter AccountID = Alice Checking"| STMT
    LOG -->|"then sum by direction"| BAL
    LOG -->|"filter AccountID = Bob Checking"| BOBSTMT
```

This is the whole idea in one picture: **one log, many filtered views.** Alice's
$100 credit and her $30 debit are interleaved in the same log as Bob's and the
vault's entries — they're never collected into an "Alice ledger." They're found by
their tag when someone asks.

> Why does Liability balance = credits − debits (not debits − credits)? Because a
> Liability's *normal balance* is Credit: credits grow what the bank owes, debits
> shrink it. The code encodes this in `AccountType.NormalBalance()`
> (`ledger/types.go:50`).

---

## 6. The code paths that produce those views

### Reading the statement (filter)

`ListTransactionsForAccount` literally scans every transaction and keeps the ones
that contain an entry tagged with the account — a `WHERE AccountID = X` over the
shared log (`ledger/list.go:92`):

```go
for _, tx := range s.transactions {
    for _, e := range tx.Entries {
        if e.AccountID == accountID { // ← the filter
            result = append(result, copyTransaction(tx))
            break
        }
    }
}
```

### Reading the balance (sum)

There is **no stored balance**. `computeBookBalance` replays the entire log every
time and adds/subtracts each matching entry by direction
(`ledger/book.go:589`):

```go
for _, tx := range s.transactions {
    for _, e := range tx.Entries {
        if e.AccountID != accountID {
            continue
        }
        if e.Direction == normal { // normal direction grows the balance
            balance += e.Amount
        } else {
            balance -= e.Amount
        }
    }
}
```

### The two layers talking to each other

A deposit (or any money movement) flows from the deposit layer down into the
single ledger:

```mermaid
sequenceDiagram
    actor Teller
    participant Reg as deposit.Register
    participant Book as ledger.Book
    Teller->>Reg: Deposit $100 for Alice
    Reg->>Book: PostTransaction(Debit Vault 100, Credit AliceGL 100)
    Book->>Book: validateBalance — debits == credits?
    Book->>Book: append transaction (entries tagged with AccountID)
    Book-->>Reg: stored
    Reg-->>Teller: done
```

And reading Alice's balance always recomputes from the log, then adds the
deposit-layer adjustments (holds, overdraft) that live *outside* the ledger:

```mermaid
sequenceDiagram
    actor UI
    participant Reg as deposit.Register
    participant Book as ledger.Book
    UI->>Reg: GetBalance(Alice)
    Reg->>Book: BookBalance(AliceGL)
    Book->>Book: replay ALL transactions,<br/>sum entries where AccountID == AliceGL
    Book-->>Reg: Book = 70
    Reg->>Reg: Available = Book − Holds + OverdraftLimit
    Reg-->>UI: {Book: 70, Holds, Available}
```

(`deposit/register.go:438` for `GetBalance`.)

---

## 7. How real banks differ — two patterns

Your earlier question — "are they stored separately, or filtered from the backing
subledger?" — maps onto a real architectural choice banks have made historically.

### Pattern A — separate books (legacy / classic core banking)

The **General Ledger** and the **deposit subledger** are genuinely *separate
systems*. The subledger holds per-customer detail; the GL holds only a single
**control account** ("Customer Deposits") whose stored balance is supposed to
equal the sum of all the detail. The subledger posts *summarized* entries up to
the GL.

```mermaid
flowchart TD
    subgraph SUB["Deposit subledger (separate system) — the detail"]
        a["Alice: 70"]
        b["Bob: 50"]
    end
    subgraph GLP["General Ledger — the summary"]
        ctrl["Customer Deposits<br/>CONTROL account: stored 120"]
    end
    SUB ==>|"posts summarized totals"| ctrl
    SUB -. "daily proof: 70 + 50 must equal 120" .-> ctrl

    classDef warn fill:#fee,stroke:#c66
    class ctrl warn
```

Because the control balance is **stored independently** of the detail, the two can
*drift* (bugs, partial failures, timing). So legacy banks run a **subledger-to-GL
reconciliation** every day to prove `Σ(detail) == control`. A mismatch is a
"reconciliation break" to be investigated. This is real operational toil — and it
exists precisely because the same number is written down in two places.

### Pattern B — one unified ledger (this codebase)

There is a **single** double-entry ledger. The deposit account is just a pointer
to a GL account; the subledger is a folder; and "total customer deposits" is
**computed by aggregation when asked**, never stored.

```mermaid
flowchart TD
    subgraph LEDGER["One ledger — single source of truth"]
        subgraph CD["Customer Deposits (subledger = folder)"]
            a["Alice GL acct → derived 70"]
            b["Bob GL acct → derived 50"]
        end
    end
    total["Total deposits<br/>= sum of accounts in the folder<br/>= 120 (computed on demand)"]
    CD -->|"aggregate when asked"| total

    classDef good fill:#efe,stroke:#6a6
    class total good
```

There is no second copy of the number, so **there is nothing to reconcile and
nothing that can drift.** This is the shape modern ledger systems converge on
(e.g. TigerBeetle, Modern Treasury). The price you pay is performance: every
balance read replays the whole log (`O(n)`), because you chose never to
materialize a stored balance.

| | Pattern A (legacy) | Pattern B (this repo) |
| --- | --- | --- |
| Per-customer entries | In the subledger system | In the one shared log, tagged by account |
| GL control account | A real account with a **stored** balance | Does not exist; total is derived |
| Subledger | A separate detail system | A folder/label |
| Balance | Often stored & maintained | Always recomputed from the log |
| Reconciliation | Required daily; can break | Structurally unnecessary |

---

## 8. The integrity guarantee that actually holds

If balances aren't stored, what keeps the books correct? **Balanced posting.**
Every transaction is rejected unless total debits equal total credits
(`validateBalance`, `ledger/book.go:404`). That single rule guarantees the signed
sum of *all* GL account balances is always exactly zero — the trial-balance
identity (of which the accounting equation, Assets = Liabilities + Equity, is a
corollary) holds after every post. That is the real invariant here, and
it's enforced at write time rather than checked after the fact.

```mermaid
flowchart LR
    POST["PostTransaction"] --> CHECK{"debits == credits?"}
    CHECK -->|no| REJECT["rejected — nothing stored"]
    CHECK -->|yes| APPEND["append to log"]
    APPEND --> INV["invariant preserved:<br/>sum of all balances = 0"]
```

The "Σ(deposits) == control" reconciliation from Pattern A holds here too — but
**vacuously**: there is no separately-stored control number for the detail to
disagree with.

---

## 9. The one thing that lives *outside* the ledger: holds

A **hold** (a pending card authorization) reduces what a customer can *spend*
without moving any money yet. It is not a real debit/credit, so it posts **no GL
entries** — it lives only in the deposit layer (`deposit/types.go:86`). That's why
available balance is computed in two parts:

```
Available = Book (from the ledger)  −  Holds (deposit layer)  +  OverdraftLimit
```

(`deposit/register.go:456`.)

Implication: the ledger is the single source of truth for *settled* money, but the
deposit layer carries state (holds) the ledger can't see or reconstruct. If this
system ever gets a database, holds are the one place where the two layers could
get out of sync — so they'd need their own consistency handling, even though the
ledger itself needs none.

---

## 10. Summary

- A deposit account stores **no entries and no balance** — it's a wrapper pointing
  at one Liability GL account.
- Entries are stored **once**, inside transactions, in a single shared log, each
  tagged with an `AccountID`.
- A statement is that log **filtered** by account; a balance is those entries
  **summed**. Both are derived, never stored.
- This is the **unified-ledger** pattern: no GL control account, subledger is just
  a folder, "total deposits" is an aggregation. Legacy banks instead keep a
  separate stored control balance and must reconcile it daily — this design makes
  that whole class of bug impossible.
- The enforced invariant is **balanced posting** (debits == credits); holds are
  the only money-affecting state kept outside the ledger.

### Where to look in the code

| Concept | File |
| --- | --- |
| Deposit account (wrapper) | `deposit/types.go:53` |
| GL account, entry, transaction, subledger | `ledger/types.go:109`–`164` |
| Normal-balance rule | `ledger/types.go:50` |
| Statement = filter | `ledger/list.go:92` |
| Balance = replay/sum | `ledger/book.go:589` |
| Balanced-posting check | `ledger/book.go:404` |
| Available balance (holds, overdraft) | `deposit/register.go:438` |
| Narrative background | `book/04-ledgers-subledgers-and-money.md`, `book/07-balances-and-holds.md` |
