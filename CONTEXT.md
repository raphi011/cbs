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

## The transport

**EBICS**:
The file-transfer protocol between institutions. Carries bytes and knows no
ISO 20022.
_Avoid_: the wire, the bus, the transport (bare), the mesh

**File**:
One upload or one download: an envelope carrying one document, which for a
`pacs.008` or `pacs.003` carries many transactions.
_Avoid_: message (for the bulk sense), batch

**Message**:
One ISO 20022 document. A file carries exactly one.
_Avoid_: file

**Order**:
One file transfer at an EBICS host — the unit an order id names and `HAC`
reports on. Never a payment instruction.
_Avoid_: instruction, request

**Order type**:
The three-letter code saying what a file carries: `CCT`, `CDD`, `CRT`, `CST`,
`CSI`, `CLD` up; `C53`, `HAC`, `BTD` down.
_Avoid_: message type, definition identifier

**Order id**:
What a host mints for one accepted upload, and what a problem is reported
against.
_Avoid_: message id — that is the `head.001` `BizMsgIdr`, minted by the sender

**Download queue**:
The files one host holds for one subscriber, in order. Enrolment creates it and
it is the whole of routing.
_Avoid_: inbox, mailbox, actor table, address table

**Subscriber**:
A party enrolled at a host, which is what gives it a queue. A bank is a
subscriber at two hosts; the settlement agent is a subscriber nowhere.
_Avoid_: actor, node, peer

**Enrolment**:
Giving a subscriber its download queue. A separate act from admission, and the
one that makes a bank REACHABLE.
_Avoid_: admission, registration, provisioning

**Host**:
An institution's EBICS server side. The clearing house and the settlement agent
are hosts; a member bank is not.
_Avoid_: server, endpoint

## The day

**Business date**:
What day the deployment thinks it is. One per deployment, in none of the N+2
databases.
_Avoid_: today, the clock, system date, booking date

**Booking date**:
When a transaction was recorded. A ledger fact, stamped from the business date.
_Avoid_: business date

**Value date**:
When an entry takes economic effect. Per entry, not per transaction.
_Avoid_: settlement date, business date

**Settlement day**:
A day TARGET is open, on which clearing and settlement run. A property of the
DATE.
_Avoid_: business day, settlement date, TARGET day

**Settlement date**:
When a payment's position settles between banks. A property of the PAYMENT.
_Avoid_: settlement day, value date

**Business day**:
One run of the day engine: every phase, on one date, settlement day or not.
_Avoid_: settlement day, clearing day

**Closure**:
Why TARGET is shut on a date — a weekend, or one of the six named holidays.
_Avoid_: holiday (bare), non-business day

**Day report**:
What one business day moved: the files, the per-transaction outcomes, and the
problems.
_Avoid_: drain, dead letters

**Problem**:
A file an institution could not process, named against the order id it arrived
under.
_Avoid_: dead letter, error (bare), failure

## Accumulating, cutting off, releasing

**Payment hub**:
Where a bank's own instructions wait between submission and its cut-off. In
memory, and one per bank.
_Avoid_: queue, outbox, batch, pending payments

**Cut-off (a bank's)**:
Turning the hub into one file per scheme and uploading it. Phase 1 of a
clearing day.
_Avoid_: cut-off (bare) where the other is meant

**Cut-off (the clearing house's)**:
Closing every open cycle, netting it and instructing settlement. Phase 3, two
phases later.
_Avoid_: cut-off (bare) where the other is meant

**Share**:
One receiving bank's transactions, cut out of one submitted file by creditor
agent.
_Avoid_: output file (until it is released), batch, slice

**Released output file**:
A share put into its bank's download queue, which happens only once the cycle
carrying it has settled.
_Avoid_: relayed message, forwarded payment

**Held return**:
A `pacs.004` the clearing house keeps in memory until the settlement agent has
answered it.
_Avoid_: pending return, queued return

## Money a bank cannot pass on

**Unapplied payment**:
A SETTLED payment the receiving bank cannot give to the customer the message
names. It is parked and returned, never refused.
_Avoid_: rejected payment, failed payment, unclaimed balance

**Unclaimed balance**:
The account a push's unapplied money is parked in, and where an ageing report
reads from.
_Avoid_: suspense, unapplied payment

**Returns receivable**:
The account a pull's unapplied money is parked in — the bank standing in for a
payer it could not collect from.
_Avoid_: unclaimed balance, suspense

**Reject**:
A refusal made BEFORE settlement, on a `pacs.002`. Only the clearing house makes
one.
_Avoid_: return, refuse

**Return**:
A reversal of a SETTLED payment, on a `pacs.004`. Every receiving bank's
objection is one.
_Avoid_: reject, recall, reversal

**Answerable**:
Whether an error is a judgement about the instruction rather than a failure of
this system's own bookkeeping. Only an answerable one becomes a return.
_Avoid_: retryable, fatal

---

**Unresolved.** Three collisions are known and not yet ruled on, so no term
above depends on them:

- **reserve account / settlement account** — the settlement agent's
  `Reserve: <Bank> (<asset>)` and the member's own mirror of it are two rows,
  and both words are currently used for both.
- **member / participant** — both live. `participant` is the id and the wire
  shape; `member` is what a bank is once a scheme has admitted it. It no longer
  also names a STATUS, that column having gone, which is one of three senses
  retired rather than the collision resolved.
- **central bank / settlement agent** — live in both the code and `README.md`.
