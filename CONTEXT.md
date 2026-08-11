# Core Banking System

A disambiguation table, not an explanation. One word — "account" — names four
things in this repository, and the entry dimension under a control account has
been called two. `README.md` carries every argument; this file only says which
word wins.

## The four senses of "account"

**GL account**:
A row in the chart of accounts, bound to one type and one asset for life.
_Avoid_: account (bare), ledger account

**Control account**:
A GL account that pools subsidiaries — one line standing for many, with every
entry against it naming which.

**Deposit account**:
A customer's demand-deposit. **Not** a GL account: it is a subsidiary under the
bank's customer-deposit control account.

**Reserve account**:
An account one institution holds in another's book. The bank's claim on the
settlement agent, and the settlement agent's liability to the bank, are two
separate rows in two separate books.
_See_: naming these two apart is unresolved — see the note at the foot.

## The entry dimension

**Subsidiary**:
Which holder an entry belongs to within a control account: a deposit account's
id, a facility's. An opaque string to `ledger`, stored in
`entries.subsidiary_id`.
_Avoid_: obligor, sub-account

**Position**:
A GL account, optionally narrowed to one subsidiary. Every balance is read
against one; there is no balance API taking a bare account.
_Avoid_: balance, sub-account

**Holder**:
The party a subsidiary names, where a generic is wanted. Neutral as to
direction, which is why it works on a Liability line.
_Avoid_: obligor

**Obligor**:
A party that owes. Exact on the Asset control accounts — drawn principal,
accrued interest receivable — and wrong on the Liability ones, where the bank
owes the holder. Never a synonym for subsidiary.

**Subledger**:
A subdivision of the general ledger grouping related GL accounts — "Customer
Deposits", "Loans and Advances". A level above the chart, not the entry
dimension; the near-homophony with _subsidiary_ is why both are listed.
_Avoid_: subsidiary ledger

**Slot**:
The role an account plays in a posting flow. A row keyed by
`(product, slot, asset)` says which account fills it.
_Avoid_: well-known account

**Facility**:
A term loan or a revolving credit line. A subsidiary under three control
accounts, and not a GL account either.

---

**Unresolved.** Two collisions are known and not yet ruled on, so no term above
depends on them:

- **reserve account / settlement account** — the settlement agent's
  `Reserve: <Bank> (<asset>)` and the member's own mirror of it are two rows,
  and both words are currently used for both.
- **member / participant**, **central bank / settlement agent** — each pair is
  live in both the code and `README.md`.
